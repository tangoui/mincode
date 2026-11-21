(() => {
 const kind=__ARG0__,limit=__ARG1__;
 const state=window.__mincodeDiagnostics;if(!state)return [];
 const items=(kind==='network_failures'?state.network.filter(item=>!item.ok):state[kind])||[];
 return items.slice(-limit);
})()
