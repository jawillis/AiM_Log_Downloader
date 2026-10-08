// SPDX-License-Identifier: GPL-3.0-or-later
//
// Added to the Lapline page by mychron-sync. Opens the sessions named in the
// link (?open=a_0764_Hallett.xrk&open=...) by fetching them from this add-on
// and handing them to Lapline, and adds a link back to the sessions page.
// All URLs are relative so this works behind Home Assistant's ingress path.
(function () {
  'use strict';

  var bar = document.querySelector('.topbar');
  if (bar) {
    var back = document.createElement('a');
    back.href = '../';
    back.textContent = '\u2190 Sessions';
    back.className = 'btn';
    back.style.textDecoration = 'none';
    back.style.marginRight = '12px';
    bar.insertBefore(back, bar.firstChild);
  }

  var names = new URLSearchParams(location.search).getAll('open');
  if (!names.length) return;

  function note(text, isError) {
    var n = document.createElement('div');
    n.setAttribute('role', 'status');
    n.textContent = text;
    n.style.cssText = 'position:fixed;left:50%;bottom:16px;transform:translateX(-50%);z-index:9999;' +
      'max-width:90vw;padding:10px 16px;border-radius:6px;font:14px system-ui,sans-serif;color:#fff;' +
      'background:' + (isError ? '#cc3a2b' : '#3f4b57');
    document.body.appendChild(n);
    setTimeout(function () { n.remove(); }, isError ? 12000 : 3000);
  }

  // If a future Lapline no longer exposes this, say so instead of failing silently.
  if (typeof App === 'undefined' || typeof App.loadBuffer !== 'function') {
    note('This version of Lapline cannot open sessions from a link. Use Open file instead.', true);
    return;
  }

  var base = new URL('../files/', location.href); // /lapline/ -> /files/
  (async function () {
    for (var i = 0; i < names.length; i++) {
      var name = names[i];
      try {
        var res = await fetch(new URL(encodeURIComponent(name), base));
        if (!res.ok) throw new Error('could not load (HTTP ' + res.status + ')');
        App.loadBuffer(await res.arrayBuffer(), name);
      } catch (e) {
        note(name + ': ' + e.message, true);
      }
    }
  })();
})();
