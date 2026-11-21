// smoketest 在真实 tango WKWebView 里端到端验证 MinCode 宿主（internal/host）：
// 页面只用 tango 注入的运行时 window.tango，覆盖
//
//   - 后端命令调用、错误、可取消调用（取消流即取消 Go 侧 ctx）；
//   - 原生能力（窗口尺寸，作用于发起调用的窗口）；
//   - 后端流（ipc 双向流，字符串帧与字节帧）；
//   - 事件下行：顺序、内联上限附近和大事件按引用经 /_mincode/event-ref 取回、
//     每个窗口各自的引用（取引用的请求必须带着正确的窗口标识）；
//   - 页面 emit 的事件经 Go 广播到所有窗口。
//
// 开两个窗口（模拟主窗口 + 插件浮窗）。运行：TANGO_LAUNCH_BACKGROUND=1 go run ./smoketest
// （窗口不激活，不抢前台）。通过时打印 SMOKE: PASS 并以 0 退出。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing/fstest"
	"time"

	"mincode/app/internal/host"

	"github.com/tangoui/tango"
	"github.com/tangoui/tango/ipc"
)

const total = 400

// 第 i 个事件的载荷长度：每 25 个一个 64 KiB 大事件、一个刚过内联上限的事件，其余是小事件。
func sizeOf(i int) int {
	switch i % 25 {
	case 7:
		return 65536
	case 13:
		return 8200
	}
	return 16
}

var page = `<!doctype html><html><head><meta charset="utf-8"></head><body>
<script>
function sizeOf(i){if(i%25===7)return 65536;if(i%25===13)return 8200;return 16;}
(async () => {
  const key = new URLSearchParams(location.search).get("k");
  const report = (s) => fetch("/smoke-report?k=" + key, {method:"POST", body:s}).catch(()=>{});
  const errs = [];
  try {
    const t = window.tango;
    if (!t) throw new Error("window.tango missing");

    // 事件：按到达顺序分发，引用项先取回载荷（同 vue-app/src/services/desktop-events.ts）。
    const got = []; let fromPage = 0; const q = []; let busy = false;
    const deliver = (e) => {
      if (e.name === "seq") { if (e.data.blob.length !== sizeOf(e.data.i)) errs.push("size " + e.data.i); got.push(e.data.i); }
      else if (e.name === "from-page") fromPage++;
      else if (e.name === "done") finish();
    };
    const drain = async () => {
      while (q.length) {
        const it = q.shift();
        if (it.ref) {
          const r = await fetch("/_mincode/event-ref?id=" + encodeURIComponent(it.ref), {cache:"no-store"});
          if (!r.ok) { errs.push("ref " + r.status); continue; }
          deliver(await r.json());
        } else deliver(it);
      }
      busy = false;
    };
    const push = (it) => { q.push(it); if (!busy) { busy = true; drain(); } };
    t.onRaw((m) => {
      if (m.startsWith("wev:")) push(JSON.parse(m.slice(4)));
      else if (m.startsWith("wref:")) push({ref: m.slice(5)});
    });
    const finish = () => {
      for (let i = 0; i < got.length; i++) if (got[i] !== i) { errs.push("order at " + i + ": " + got[i]); break; }
      if (got.length !== ` + fmt.Sprint(total) + `) errs.push("count " + got.length);
      if (fromPage !== 2) errs.push("from-page " + fromPage);
      report(errs.length ? "FAIL " + errs.slice(0, 5).join("; ") : "PASS");
    };

    // 调用。
    const echo = await t.call("mincode.invoke", {command: "echo", args: {a: 1}});
    if (JSON.stringify(echo) !== '{"a":1}') errs.push("echo " + JSON.stringify(echo));
    try { await t.call("mincode.invoke", {command: "fail"}); errs.push("fail resolved"); }
    catch (e) { if (!String(e && e.message).includes("boom")) errs.push("fail message " + e); }

    // 可取消调用：取消流后 Go 侧命令的 ctx 被取消。
    const s = t.stream("mincode.invokeStream", {command: "wait", args: {k: key}});
    setTimeout(() => s.cancel(), 100);
    try { for await (const v of s) errs.push("wait yielded " + v); } catch (e) {}
    const waited = await t.call("mincode.invoke", {command: "waitStatus", args: {k: key}});
    if (waited !== "canceled") errs.push("wait status " + waited);

    // 原生能力：本窗口的尺寸。
    const size = await t.call("mincode.native", {op: "window.size"});
    if (!(size.width > 0 && size.height > 0)) errs.push("size " + JSON.stringify(size));

    // 流：字符串帧和字节帧都按字节回显。
    const sock = t.socket("smoke-echo");
    sock.binaryType = "arraybuffer";
    const echoed = [];
    const twoFrames = new Promise((resolve) => sock.addEventListener("message", (e) => {
      echoed.push(new TextDecoder().decode(e.data)); if (echoed.length === 2) resolve();
    }));
    sock.send("ping");
    sock.send(new TextEncoder().encode("bytes"));
    await Promise.race([twoFrames, new Promise((r) => setTimeout(r, 3000))]);
    sock.close();
    if (echoed.join(",") !== "ping,bytes") errs.push("stream " + echoed.join(","));

    // 通知 Go 开始发事件；本页再 emit 一个，两个窗口都应收到。
    t.emit("mincode.emit", {name: "from-page", data: key});
    await fetch("/smoke-ready?k=" + key, {method: "POST"});
  } catch (e) {
    report("FAIL " + (e && e.message ? e.message : e));
  }
})();
</script></body></html>`

type nativeStub struct{ windows map[host.Page]host.Window }

func (nativeStub) ClipboardText() string                               { return "" }
func (nativeStub) SetClipboardText(string)                             {}
func (nativeStub) OpenDialog(host.OpenDialogOptions) ([]string, error) { return nil, nil }
func (nativeStub) SaveDialog(host.SaveDialogOptions) (string, error)   { return "", nil }
func (nativeStub) MessageDialog(string, host.MessageDialogOptions) (string, error) {
	return "OK", nil
}
func (nativeStub) OpenExternal(string) error                   { return nil }
func (nativeStub) IsDarkMode() bool                            { return false }
func (nativeStub) Quit()                                       {}
func (nativeStub) HideApp()                                    {}
func (nativeStub) ShowApp()                                    {}
func (nativeStub) Notify(string, string, map[string]any) error { return nil }

type refStat struct{ ok, bad int }

func main() {
	fsys := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte(page)}}

	var mu sync.Mutex
	waitStatus := map[string]string{}
	refs := map[string]*refStat{} // 窗口 key → 取引用请求统计
	results := make(chan [2]string, 4)
	ready := make(chan string, 4)

	dispatch := func(ctx context.Context, cmd, args string) (string, error) {
		var a struct {
			K string `json:"k"`
		}
		_ = json.Unmarshal([]byte(args), &a)
		switch cmd {
		case "echo":
			return args, nil
		case "fail":
			return "", fmt.Errorf("boom")
		case "wait":
			select {
			case <-ctx.Done():
				mu.Lock()
				waitStatus[a.K] = "canceled"
				mu.Unlock()
				return "", ctx.Err()
			case <-time.After(5 * time.Second):
				return `"timeout"`, nil
			}
		case "waitStatus":
			mu.Lock()
			defer mu.Unlock()
			b, _ := json.Marshal(waitStatus[a.K])
			return string(b), nil
		}
		return "", fmt.Errorf("unknown command %s", cmd)
	}

	var bridge http.Handler
	app := tango.New(tango.Options{
		Name: "smoketest", ID: "dev.mincode.smoketest",
		Assets: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			key := r.Header.Get(host.SurfaceHeader)
			switch r.URL.Path {
			case "/smoke-report":
				b, _ := io.ReadAll(r.Body)
				results <- [2]string{r.URL.Query().Get("k"), string(b)}
				return
			case "/smoke-ready":
				ready <- r.URL.Query().Get("k")
				return
			case "/_mincode/event-ref":
				rec := &statusRW{ResponseWriter: w, code: 200}
				bridge.ServeHTTP(rec, r)
				mu.Lock()
				st := refs[key]
				if st == nil {
					st = &refStat{}
					refs[key] = st
				}
				if rec.code == 200 {
					st.ok++
				} else {
					st.bad++
				}
				mu.Unlock()
				return
			}
			bridge.ServeHTTP(w, r)
		}),
	})
	router := ipc.NewRouter()
	host.Attach(app, router)
	native := host.NewTangoNative(app, nil)
	h := host.New(native, dispatch, router)
	h.RegisterStream("smoke-echo", func(conn host.StreamConn) {
		for {
			data, err := conn.Receive()
			if err != nil {
				return
			}
			if err := conn.Send(data); err != nil {
				return
			}
		}
	})
	bridge = native.AssetsHandler(h, fsys, "")

	keys := []string{"main", "popup"}
	app.OnReady(func() {
		for i, k := range keys {
			s, err := app.NewSurface(tango.SurfaceOptions{
				Title: "smoketest " + k, URL: "/?k=" + k, Width: 400, Height: 300,
				X: 40 + i*420, Y: 40,
				WebView: tango.WebViewOptions{Headers: http.Header{host.SurfaceHeader: []string{k}}},
			})
			if err != nil {
				log.Fatal(err)
			}
			native.AddSurface(h, s, k)
		}
	})

	go func() {
		deadline := time.After(30 * time.Second)
		for n := 0; n < len(keys); n++ {
			select {
			case <-ready:
			case r := <-results:
				fmt.Printf("SMOKE %s: page=%q\nSMOKE: FAIL\n", r[0], r[1])
				os.Exit(1)
			case <-deadline:
				fmt.Println("SMOKE: TIMEOUT waiting for pages")
				os.Exit(1)
			}
		}
		start := time.Now()
		bigPerPage := 0
		for i := 0; i < total; i++ {
			if sizeOf(i) > 8192 {
				bigPerPage++
			}
			h.Emit("seq", map[string]any{"i": i, "blob": strings.Repeat("x", sizeOf(i))})
		}
		h.Emit("done", nil)

		got := map[string]string{}
		for len(got) < len(keys) {
			select {
			case r := <-results:
				got[r[0]] = r[1]
			case <-deadline:
				fmt.Println("SMOKE: TIMEOUT waiting for results", got)
				os.Exit(1)
			}
		}
		elapsed := time.Since(start)
		pass := true
		mu.Lock()
		for _, k := range keys {
			st := refs[k]
			if st == nil {
				st = &refStat{}
			}
			ok := got[k] == "PASS" && st.ok == bigPerPage && st.bad == 0
			pass = pass && ok
			fmt.Printf("SMOKE %s: page=%q refs ok=%d bad=%d (want ok=%d)\n", k, got[k], st.ok, st.bad, bigPerPage)
		}
		for k, st := range refs {
			if k != "main" && k != "popup" {
				pass = false
				fmt.Printf("SMOKE: ref requests without a window key: key=%q ok=%d bad=%d\n", k, st.ok, st.bad)
			}
		}
		mu.Unlock()
		fmt.Printf("SMOKE timing: %d events delivered to both windows in %s\n", total+1, elapsed)
		if pass {
			fmt.Println("SMOKE: PASS")
			os.Exit(0)
		}
		fmt.Println("SMOKE: FAIL")
		os.Exit(1)
	}()
	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

type statusRW struct {
	http.ResponseWriter
	code int
}

func (s *statusRW) WriteHeader(c int) { s.code = c; s.ResponseWriter.WriteHeader(c) }
