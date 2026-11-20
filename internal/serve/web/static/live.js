'use strict';
// The page's rule for a new dev serve version, without the DOM, so a test runs it in node (live_test.go).

const RELOAD_GAP_MS = 60000;

// versionStep is what the page does with the version a board names, the page having loaded with loaded ('' before
// its first board): 'keep' it, 'reload' the page, or 'offer' a reload, while busy (typed or picked in an open sheet)
// or within RELOAD_GAP_MS of this tab's last reload (lastReload, ms; 0: none), so versions that keep differing
// never reload it in a loop.
function versionStep(loaded, seen, busy, lastReload, now) {
  if (!loaded || seen === loaded) return 'keep';
  if (busy || (lastReload > 0 && now - lastReload < RELOAD_GAP_MS)) return 'offer';
  return 'reload';
}
