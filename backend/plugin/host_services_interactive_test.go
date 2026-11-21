package plugin

import (
	"testing"
	"time"

	"mincode/app/plugins/api"
)

// interactive:false on host.services.invoke/start: a call that would need
// any prompt (the approval, or the provider picker) fails at once with
// interaction_required. It never lets through a call that would have been
// confirmed; only full trust or a remembered read grant pass.

const nonInteractiveCall = `{"service":"demo.text.reverse","provider":{"pluginId":"service.short"},"input":"x","timeoutMs":5000,"interactive":false}`

// interactiveFixture is a provider service.short (entry risk given) and a
// trustable caller.a with the plugins capability, in one manager.
type interactiveFixture struct {
	events    *capturedUIEvents
	provider  *serviceCallFixture
	caller    *processRuntime
	setLocked func(bool)
}

func newInteractiveFixture(t *testing.T, risk string) *interactiveFixture {
	t.Helper()
	events := captureHostUI(t)
	entry := serviceTestEntry()
	entry.Risk = risk
	provider := serviceCallProvider(t, "service.short", entry, 16384)
	setLocked := unlockAppForTest(t)
	makeServiceProviderTrustable(t, provider)
	addTrustablePlugin(t, provider.manager, "caller.a", []api.Capability{api.CapabilityPlugins}, ManifestUIAction{ID: "ping", Risk: "read", AICallable: true})
	useServicesManager(t, provider.manager)
	caller := servicesCaller("caller.a", api.CapabilityPlugins)
	caller.ownerManager = provider.manager
	return &interactiveFixture{events: events, provider: provider, caller: caller, setLocked: setLocked}
}

// callerServiceCalls counts the service calls caller owns.
func callerServiceCalls(caller string) int {
	serviceCalls.mu.Lock()
	defer serviceCalls.mu.Unlock()
	count := 0
	for _, call := range serviceCalls.calls {
		if !call.host && call.owner == caller {
			count++
		}
	}
	return count
}

// wantInteractionRequired runs params through invoke and start and checks
// that both fail fast with interaction_required, show no prompt, run
// nothing, audit nothing and leave the caller's grants alone.
func (fixture *interactiveFixture) wantInteractionRequired(t *testing.T, params string) {
	t.Helper()
	grantsBefore := readPluginCallGrants("caller.a").Targets
	// Earlier tests may leave finished caller.a calls in the registry.
	callsBefore := callerServiceCalls("caller.a")
	for name, handler := range map[string]hostMethodHandler{"invoke": hostServicesInvoke, "start": hostServicesStart} {
		stop := refusePrompts(t, fixture.events)
		started := time.Now()
		_, rpcErr := servicesCall(t, handler, fixture.caller, params)
		elapsed := time.Since(started)
		stop()
		if rpcErrorCode(rpcErr) != contributionErrInteractionRequired || rpcErr.Code != -32000 ||
			rpcErr.Message != "interaction_required: the call needs the user's confirmation" {
			t.Fatalf("%s: err = %+v", name, rpcErr)
		}
		if elapsed >= time.Second {
			t.Fatalf("%s took %v", name, elapsed)
		}
		noPromptEvents(t, fixture.events)
	}
	if count := callerServiceCalls("caller.a"); count != callsBefore {
		t.Fatalf("service calls = %d, before %d", count, callsBefore)
	}
	if providerStarted(fixture.provider) {
		t.Fatal("provider was started")
	}
	for _, id := range []string{"caller.a", "service.short"} {
		if entries := skippedAudit(t, id); len(entries) != 0 {
			t.Fatalf("%s audited: %+v", id, entries)
		}
	}
	if grants := readPluginCallGrants("caller.a").Targets; len(grants) != len(grantsBefore) {
		t.Fatalf("grants %v, before %v", grants, grantsBefore)
	}
}

func TestServicesNonInteractiveNeverPrompts(t *testing.T) {
	t.Run("write_untrusted", func(t *testing.T) {
		fixture := newInteractiveFixture(t, "write")
		fixture.wantInteractionRequired(t, nonInteractiveCall)
	})
	t.Run("read_not_granted", func(t *testing.T) {
		fixture := newInteractiveFixture(t, "read")
		fixture.wantInteractionRequired(t, nonInteractiveCall)
	})
	t.Run("locked", func(t *testing.T) {
		fixture := newInteractiveFixture(t, "write")
		grantFullTrustFor(t, fixture.provider.manager, "service.short")
		grantFullTrustFor(t, fixture.provider.manager, "caller.a")
		fixture.setLocked(true)
		fixture.wantInteractionRequired(t, nonInteractiveCall)
	})
}

func TestServicesNonInteractiveUnderFullTrust(t *testing.T) {
	for _, trusted := range []string{"service.short", "caller.a"} {
		t.Run(trusted, func(t *testing.T) {
			fixture := newInteractiveFixture(t, "write")
			grantFullTrustFor(t, fixture.provider.manager, trusted)
			stop := refusePrompts(t, fixture.events)
			result, rpcErr := servicesCall(t, hostServicesInvoke, fixture.caller, nonInteractiveCall)
			stop()
			if rpcErr != nil {
				t.Fatal(rpcErr)
			}
			if result["output"] != "done" {
				t.Fatalf("result = %v", result)
			}
			noPromptEvents(t, fixture.events)
			counterpart := "caller.a"
			if trusted == "caller.a" {
				counterpart = "service.short"
			}
			entries := skippedAudit(t, trusted)
			if len(entries) != 1 || entries[0].Surface != "serviceCall" || entries[0].Risk != "write" ||
				entries[0].Subject != "demo.text.reverse" || entries[0].Counterpart != counterpart {
				t.Fatalf("audit = %+v", entries)
			}
		})
	}
}

func TestServicesNonInteractiveReadGranted(t *testing.T) {
	fixture := newInteractiveFixture(t, "read")
	if err := grantPluginCall("caller.a", "service.short"); err != nil {
		t.Fatal(err)
	}
	stop := refusePrompts(t, fixture.events)
	result, rpcErr := servicesCall(t, hostServicesInvoke, fixture.caller, nonInteractiveCall)
	stop()
	if rpcErr != nil {
		t.Fatal(rpcErr)
	}
	if result["output"] != "done" {
		t.Fatalf("result = %v", result)
	}
	noPromptEvents(t, fixture.events)
}

func TestServicesNonInteractiveRefusesPicker(t *testing.T) {
	fixture := newInteractiveFixture(t, "write")
	addServiceProcessProvider(t, fixture.provider.manager, "service.tasks", "TestServiceHelperProcess", serviceTestEntry(), 16384)
	// Even a trusted caller does not get the picker skipped.
	grantFullTrustFor(t, fixture.provider.manager, "caller.a")
	unpinned := `{"service":"demo.text.reverse","input":"x","timeoutMs":5000,"interactive":false}`
	for name, handler := range map[string]hostMethodHandler{"invoke": hostServicesInvoke, "start": hostServicesStart} {
		stop := refusePrompts(t, fixture.events)
		_, rpcErr := servicesCall(t, handler, fixture.caller, unpinned)
		stop()
		if rpcErrorCode(rpcErr) != contributionErrInteractionRequired || rpcErr.Code != -32000 ||
			rpcErr.Message != interactionRequiredError().Message {
			t.Fatalf("%s: err = %+v", name, rpcErr)
		}
		noPromptEvents(t, fixture.events)
	}
	if defaults := readServiceDefaults(); len(defaults) != 0 {
		t.Fatalf("defaults = %v", defaults)
	}
	// Interactive, the picker is still shown.
	go answerNext(t, fixture.events, func(event promptEvent) promptReply {
		if event.Kind != serviceProviderPromptKind || len(event.Items) != 2 {
			t.Errorf("prompt %+v", event)
		}
		return promptReply{Cancelled: true}
	})
	if _, rpcErr := servicesCall(t, hostServicesInvoke, fixture.caller, `{"service":"demo.text.reverse","input":"x","timeoutMs":5000,"interactive":true}`); rpcErrorCode(rpcErr) != "declined" {
		t.Fatalf("interactive picker: %v", rpcErr)
	}
}

func TestServicesInteractiveParamStrict(t *testing.T) {
	fixture := newInteractiveFixture(t, "write")
	for _, value := range []string{`"no"`, `0`, `"false"`} {
		params := `{"service":"demo.text.reverse","provider":{"pluginId":"service.short"},"input":"x","interactive":` + value + `}`
		_, rpcErr := servicesCall(t, hostServicesInvoke, fixture.caller, params)
		if rpcErrorCode(rpcErr) != "invalid_params" {
			t.Fatalf("interactive %s: %v", value, rpcErr)
		}
	}
	for _, params := range []string{
		`{"service":"demo.text.reverse","provider":{"pluginId":"service.short"},"input":"x","timeoutMs":5000,"interactive":true}`,
		`{"service":"demo.text.reverse","provider":{"pluginId":"service.short"},"input":"x","timeoutMs":5000}`,
	} {
		go answerNext(t, fixture.events, func(event promptEvent) promptReply {
			if event.Kind != "pluginCall" || event.Call == nil || event.Call.Service != "demo.text.reverse" {
				t.Errorf("prompt %+v", event)
			}
			return promptReply{Cancelled: true}
		})
		if _, rpcErr := servicesCall(t, hostServicesInvoke, fixture.caller, params); rpcErrorCode(rpcErr) != "declined" {
			t.Fatalf("%s: %v", params, rpcErr)
		}
	}
}
