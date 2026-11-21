(() => {
 // Windows only: WebView2 history goes through the page; macOS uses WKWebView goBack/goForward.
 const delta=__ARG0__;
 if(delta<0)history.back();else history.forward();
 return true;
})()
