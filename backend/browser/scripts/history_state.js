(() => {
 const token=__ARG0__,mark=__ARG1__;
 // goHistory (automation.go) labels the current document with a token before going through history; a document reached through history does not carry it, even at the same URL.
 if(mark)Object.defineProperty(window,'__mincodeHistoryMark',{value:token,configurable:true,writable:true,enumerable:false});
 return {readyState:document.readyState,url:location.href,title:document.title,marked:window.__mincodeHistoryMark===token};
})()
