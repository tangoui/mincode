package debug

import "strings"

// dapStandardNames lists the request (including reverse request) and event
// names of the Debug Adapter Protocol specification
// (https://microsoft.github.io/debug-adapter-protocol/specification), lower
// cased. A plugin may declare custom DAP names (for example a debugger's hot
// code replace event and request) only outside this list.
var dapStandardNames = func() map[string]bool {
	names := []string{
		// Requests.
		"attach", "breakpointLocations", "cancel", "completions", "configurationDone", "continue",
		"dataBreakpointInfo", "disassemble", "disconnect", "evaluate", "exceptionInfo", "goto",
		"gotoTargets", "initialize", "launch", "loadedSources", "locations", "modules", "next", "pause",
		"readMemory", "restart", "restartFrame", "reverseContinue", "scopes", "setBreakpoints",
		"setDataBreakpoints", "setExceptionBreakpoints", "setExpression", "setFunctionBreakpoints",
		"setInstructionBreakpoints", "setVariable", "source", "stackTrace", "stepBack", "stepIn",
		"stepInTargets", "stepOut", "terminate", "terminateThreads", "threads", "variables", "writeMemory",
		// Reverse requests.
		"runInTerminal", "startDebugging",
		// Events.
		"breakpoint", "capabilities", "continued", "exited", "initialized", "invalidated", "loadedSource",
		"memory", "module", "output", "process", "progressEnd", "progressStart", "progressUpdate",
		"stopped", "terminated", "thread",
	}
	set := make(map[string]bool, len(names))
	for _, name := range names {
		set[strings.ToLower(name)] = true
	}
	return set
}()

// IsStandardDAPName reports whether name is a DAP standard request or event
// name, compared case-insensitively.
func IsStandardDAPName(name string) bool {
	return dapStandardNames[strings.ToLower(name)]
}
