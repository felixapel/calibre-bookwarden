// Keyboard decisions for the Fixes page (ADR-034, carried decisions).
// j/k move the focused row, a/r press that row's own Approve or Reject
// button (the individual decision), s skips to the next row and ? shows the
// help. Nothing here decides anything itself: it only clicks the buttons the
// page already has, one row at a time. Without this file the page works the
// same with the mouse.
(function () {
  "use strict";

  var rows = Array.prototype.slice.call(document.querySelectorAll("tr[data-row]"));
  if (rows.length === 0) {
    return;
  }
  var help = document.getElementById("inbox-help");
  var busy = false; // a decision was sent; the page reloads, so no second one
  var NEXT = "inbox-next"; // sessionStorage key: the row to focus after the reload

  function remember(id) {
    try {
      if (id) {
        sessionStorage.setItem(NEXT, id);
      } else {
        sessionStorage.removeItem(NEXT);
      }
    } catch (e) {
      // Storage may be off; the page still works, focus just starts over.
    }
  }

  // After a decision the page reloads: focus the row after the one acted on.
  (function restore() {
    var id = null;
    try {
      id = sessionStorage.getItem(NEXT);
    } catch (e) {
      return;
    }
    remember(null);
    var row = rows.filter(function (r) { return r.getAttribute("data-row") === id; })[0];
    if (row) {
      row.focus();
    }
  })();

  // typing reports whether focus is somewhere text is entered.
  function typing(el) {
    if (!el || el === document.body) {
      return false;
    }
    var tag = el.tagName;
    return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT" || el.isContentEditable;
  }

  // focusedRow is the row that holds keyboard focus, or null.
  function focusedRow() {
    var el = document.activeElement;
    var row = el && el.closest ? el.closest("tr[data-row]") : null;
    return row && rows.indexOf(row) >= 0 ? row : null;
  }

  function move(step) {
    var row = focusedRow();
    var at = row ? rows.indexOf(row) + step : step > 0 ? 0 : rows.length - 1;
    if (at >= 0 && at < rows.length) {
      rows[at].focus();
      rows[at].scrollIntoView({ block: "nearest" });
    }
  }

  // decide clicks the focused row's own button for the action; it never
  // looks at any other row.
  function decide(action) {
    var row = focusedRow();
    if (!row || busy) {
      return;
    }
    var button = row.querySelector('button[data-act="' + action + '"]');
    if (button && !button.disabled) {
      busy = true;
      var next = rows[rows.indexOf(row) + 1];
      remember(next ? next.getAttribute("data-row") : null);
      button.click();
    }
  }

  function toggleHelp(show) {
    if (help) {
      help.hidden = show === undefined ? !help.hidden : !show;
    }
  }

  document.addEventListener("keydown", function (event) {
    if (event.defaultPrevented || event.ctrlKey || event.metaKey || event.altKey || typing(event.target)) {
      return;
    }
    switch (event.key) {
      case "j":
        move(1);
        break;
      case "k":
        move(-1);
        break;
      case "a":
      case "r":
        if (event.repeat) {
          return;
        }
        decide(event.key === "a" ? "approve" : "reject");
        break;
      case "s":
        if (focusedRow()) {
          focusedRow().setAttribute("data-skipped", "");
        }
        move(1);
        break;
      case "?":
        toggleHelp();
        break;
      case "Escape":
        toggleHelp(false);
        return;
      default:
        return;
    }
    event.preventDefault();
  });
})();
