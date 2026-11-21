// Loaded only by the native -tags e2e smoke scenarios. The selectors mirror
// DatabaseConnectionOverview.vue ("All connections" table); the overview source
// test asserts they still exist so a markup change cannot silently break the smoke.

/** The "Open" action of a saved connection's row; it connects when offline. */
export function overviewConnectAction(connectionId: string): HTMLButtonElement | undefined {
  const row = [...document.querySelectorAll<HTMLElement>(".db-connection-overview__connections .connection-overview__row")]
    .find((element) => element.querySelector(".connection-overview__name")?.textContent?.trim() === connectionId);
  return row?.querySelector<HTMLButtonElement>("button.db-connection-overview__action") ?? undefined;
}
