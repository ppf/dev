'use strict';
// The page's rules without the DOM, so a test runs them in node (live_test.go): a new dev serve version, a sheet's title.

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

const TITLE_MAX = 120;
const count = (s, re) => (s.match(re) || []).length;
// unclosed is whether s leaves a bracket or quote open.
const unclosed = s => count(s, /[(\[„“]/g) > count(s, /[)\]”]/g) || count(s, /"/g) % 2 === 1;
// An abbreviation's dot ends no sentence
const ABBREV = /\b(?:e\.g|i\.e|vs|cf|approx|no)\.$/i;

// lead splits an item's text, trimmed, for its sheet into [title, rest]: its first line when that is TITLE_MAX
// characters or fewer, else a text that short whole, else the text up to the first sentence end (one followed by a
// capital or digit, not after "e.g.") or, failing that, clause end (": " or "; ") that falls 20 to TITLE_MAX
// characters in, outside brackets and quotes; rest without the whitespace around the cut. A text with none of these
// has no title: ['', text].
function lead(text) {
  text = text.replace(/\r\n?/g, '\n').trim();
  const nl = text.indexOf('\n');
  if (nl >= 0 && nl <= TITLE_MAX) return [text.slice(0, nl).trimEnd(), text.slice(nl + 1).trim()];
  if (nl < 0 && text.length <= TITLE_MAX) return [text, ''];
  for (const end of [/[.?!][)"”’]?(?=\s+[\p{Lu}\p{N}])/gu, /[:;](?=\s)/g]) {
    for (const m of text.matchAll(end)) {
      const cut = m.index + m[0].length;
      if (cut > TITLE_MAX) break;
      const title = text.slice(0, cut);
      if (cut >= 20 && !unclosed(title) && !ABBREV.test(title)) return [title, text.slice(cut).trim()];
    }
  }
  return ['', text];
}
