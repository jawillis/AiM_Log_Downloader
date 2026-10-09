// SPDX-License-Identifier: GPL-3.0-or-later
//
// Added to the Lapline page by mychron-sync. Opens the sessions named in the
// link (?open=a_0764_Hallett.xrk&open=RaceChrono/run.vbo&...) by fetching them from this add-on
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

  // Imported .vbo files can sit in subfolders: encode each part of the path.
  function fileURL(name) { return new URL(name.split('/').map(encodeURIComponent).join('/'), base); }

  // Lapline clears the comparison when a file's track differs from the first
  // one's. A .vbo usually doesn't name its track, so when the sessions page
  // knows it (named in the file or guessed from position), write it into the
  // file's [comments] the way Lapline reads it. Only the copy in this browser
  // tab is changed, never the file on the share.
  function withTrack(buf, track) {
    // One character per byte, so string indexes are byte offsets. The header is near the top.
    var text = new TextDecoder('latin1').decode(new Uint8Array(buf, 0, Math.min(buf.byteLength, 262144)));
    var dataAt = text.search(/^\[data\]/im);
    var head = dataAt < 0 ? text : text.slice(0, dataAt);
    var line = 'Track: ' + track.replace(/[\r\n]+/g, ' ') + '\r\n';
    var at, insert;
    var m = /^\[comments\][^\n]*\n/im.exec(head);
    if (m) { at = m.index + m[0].length; insert = line; }
    else {
      at = head.search(/^\[/m);
      if (at < 0) return buf;
      insert = '[comments]\r\n' + line + '\r\n';
    }
    var add = new Uint8Array(insert.length);
    for (var i = 0; i < insert.length; i++) { var c = insert.charCodeAt(i); add[i] = c < 256 ? c : 63; }
    var src = new Uint8Array(buf), out = new Uint8Array(src.length + add.length);
    out.set(src.subarray(0, at), 0);
    out.set(add, at);
    out.set(src.subarray(at), at + add.length);
    return out.buffer;
  }

  (async function () {
    var info = {};
    if (names.some(function (n) { return /\.vbo$/i.test(n); })) {
      try {
        var list = await (await fetch(new URL('../api/sessions', location.href))).json();
        (list.sessions || []).forEach(function (e) { info[e.file] = e; });
      } catch (e) { /* the files still open, just without a track name */ }
    }
    for (var i = 0; i < names.length; i++) {
      var name = names[i];
      try {
        var res = await fetch(fileURL(name));
        if (!res.ok) throw new Error('could not load (HTTP ' + res.status + ')');
        var buf = await res.arrayBuffer(), e = info[name];
        if (e && e.source === 'vbo' && !e.track && e.track_shown) buf = withTrack(buf, e.track_shown);
        App.loadBuffer(buf, name.split('/').pop());
      } catch (e) {
        note(name + ': ' + e.message, true);
      }
    }
  })();
})();
