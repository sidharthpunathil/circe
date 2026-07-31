// Progressive enhancement only. Every action on the site works without this
// file; it just makes the forms nicer to use.
(function () {
  "use strict";

  // Copy-to-clipboard buttons.
  document.addEventListener("click", function (e) {
    var btn = e.target.closest("[data-copy]");
    if (!btn) return;
    e.preventDefault();
    var text = btn.getAttribute("data-copy");
    var restore = btn.textContent;
    var done = function (ok) {
      btn.textContent = ok ? "Copied" : "Failed";
      setTimeout(function () {
        btn.textContent = restore;
      }, 1400);
    };
    if (navigator.clipboard && window.isSecureContext) {
      navigator.clipboard.writeText(text).then(
        function () { done(true); },
        function () { done(false); }
      );
    } else {
      // Fallback for plain http:// origins, which is the common case here.
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      var ok = false;
      try { ok = document.execCommand("copy"); } catch (err) { ok = false; }
      document.body.removeChild(ta);
      done(ok);
    }
  });

  // Destructive forms confirm before submitting.
  document.addEventListener("submit", function (e) {
    var form = e.target.closest("form[data-confirm]");
    if (!form) return;
    if (!window.confirm(form.getAttribute("data-confirm"))) {
      e.preventDefault();
    }
  });

  // Live character counters on length-limited fields.
  document.querySelectorAll("[data-counter]").forEach(function (field) {
    var out = document.getElementById(field.getAttribute("data-counter"));
    if (!out) return;
    var max = field.getAttribute("maxlength") || "";
    var update = function () {
      out.textContent = field.value.length + (max ? "/" + max : "");
      out.classList.toggle("text-danger", max && field.value.length >= Number(max));
    };
    field.addEventListener("input", update);
    update();
  });

  // Checkbox pickers: filter, select all/none, and a live selected count.
  //
  // A page can carry several pickers at once (a folder holds skills, rules and
  // servers), so a picker's own count and "nothing matched" line are looked up
  // inside its [data-picker] wrapper. Without a wrapper the whole document is
  // the scope, which is what a single-picker page wants.
  function pickerRoot(el) {
    return (el && el.closest && el.closest("[data-picker]")) || document;
  }

  function countSelected(scope) {
    var boxes = scope.querySelectorAll('input[type="checkbox"]');
    var n = 0;
    boxes.forEach(function (b) { if (b.checked) n++; });
    pickerRoot(scope).querySelectorAll("[data-selected-count]").forEach(function (el) {
      el.textContent = String(n);
    });
  }

  document.querySelectorAll("[data-filter]").forEach(function (input) {
    var scope = document.querySelector(input.getAttribute("data-filter"));
    if (!scope) return;
    var emptyMsg = pickerRoot(scope).querySelector("[data-filter-empty]");
    input.addEventListener("input", function () {
      var q = input.value.trim().toLowerCase();
      var visible = 0;
      scope.querySelectorAll("[data-filter-item]").forEach(function (item) {
        var match = !q || item.getAttribute("data-filter-item").indexOf(q) !== -1;
        item.hidden = !match;
        if (match) visible++;
      });
      if (emptyMsg) emptyMsg.hidden = visible !== 0;
    });
  });

  function bindBulk(attr, checked) {
    document.querySelectorAll("[" + attr + "]").forEach(function (btn) {
      var scope = document.querySelector(btn.getAttribute(attr));
      if (!scope) return;
      btn.addEventListener("click", function () {
        // Only affect rows currently visible under the filter.
        scope.querySelectorAll('input[type="checkbox"]').forEach(function (box) {
          var row = box.closest("[data-filter-item]");
          if (row && row.hidden) return;
          box.checked = checked;
        });
        countSelected(scope);
      });
    });
  }
  bindBulk("data-select-all", true);
  bindBulk("data-select-none", false);

  document.querySelectorAll("#skill-picker, #import-picker, [data-picker-list]").forEach(function (scope) {
    scope.addEventListener("change", function () { countSelected(scope); });
    countSelected(scope);
  });

  // Select mode on the skills list: reveal per-card checkboxes and a bulk bar.
  (function bulkSelect() {
    var toggle = document.querySelector("[data-toggle-select]");
    var bar = document.querySelector("[data-bulk-bar]");
    var grid = document.getElementById("skill-grid");
    if (!toggle || !bar || !grid) return;

    var boxes = function () { return grid.querySelectorAll(".select-box"); };
    var active = false;

    function count() {
      var n = grid.querySelectorAll('input[type="checkbox"]:checked').length;
      document.querySelectorAll("[data-selected-count]").forEach(function (el) {
        el.textContent = String(n);
      });
      bar.classList.toggle("hidden", n === 0);
      bar.classList.toggle("flex", n > 0);
    }

    toggle.addEventListener("click", function () {
      active = !active;
      toggle.textContent = active ? "Done" : "Select";
      boxes().forEach(function (b) {
        b.classList.toggle("hidden", !active);
        b.classList.toggle("flex", active);
      });
      if (!active) {
        grid.querySelectorAll('input[type="checkbox"]').forEach(function (b) { b.checked = false; });
      }
      count();
    });

    grid.addEventListener("change", count);
    count();
  })();

  // MCP server form: a stdio server needs a command, an http or sse one needs a
  // URL, and showing both at once makes the form read as twice the work. With
  // this file absent every field is visible and the form still saves correctly,
  // because the server drops whatever the chosen transport does not use.
  document.querySelectorAll("[data-transport-form]").forEach(function (form) {
    var groups = form.querySelectorAll("[data-when-transport], [data-unless-transport]");
    if (!groups.length) return;
    var apply = function () {
      var picked = form.querySelector('input[name="transport"]:checked');
      var value = picked ? picked.value : "stdio";
      groups.forEach(function (group) {
        // Named either way round, so a group can say which transports it belongs
        // to or which single one it does not, and a new transport keeps working.
        var only = group.getAttribute("data-when-transport");
        var except = group.getAttribute("data-unless-transport");
        if (only !== null) group.hidden = only.split(" ").indexOf(value) === -1;
        if (except !== null) group.hidden = except.split(" ").indexOf(value) !== -1;
      });
    };
    form.addEventListener("change", function (e) {
      if (e.target && e.target.name === "transport") apply();
    });
    apply();
  });

  // Warn before navigating away from an edited form with unsaved changes.
  document.querySelectorAll("form[method='post']").forEach(function (form) {
    if (form.hasAttribute("data-confirm") || form.classList.contains("hidden")) return;
    var dirty = false;
    form.addEventListener("input", function () { dirty = true; });
    form.addEventListener("submit", function () { dirty = false; });
    window.addEventListener("beforeunload", function (e) {
      if (!dirty) return;
      e.preventDefault();
      e.returnValue = "";
    });
  });

  // Flash notices.
  //
  // The message is focused rather than left to aria-live. A live region whose
  // content arrives in the same server response has no mutation for a screen
  // reader to observe, so it announces nothing; focusing it does announce, and
  // it also puts the message on the keyboard path where it was invisible before.
  //
  // Nothing dismisses itself on a timer. The longest confirmations here report
  // counts that appear nowhere else on the page, and removing those while
  // someone is still reading loses information and shifts the layout under
  // their pointer.
  var flash = document.querySelector("[data-flash]");
  if (flash) {
    // After a redirect focus sits on <body>, so there is no user position to
    // steal. Preventing scroll keeps the viewport where the browser put it.
    flash.focus({ preventScroll: true });
  }

  document.addEventListener("click", function (e) {
    if (!e.target.closest("[data-flash-dismiss]")) return;
    var slot = e.target.closest("[data-flash-slot]");
    if (!slot) return;

    var done = false;
    var finish = function () {
      if (done) return;
      done = true;
      // Focus is on the dismiss button inside the thing being removed, and a
      // removed element drops focus to <body>, which would throw a keyboard
      // user back through the whole header. Hand it to the page heading first.
      if (slot.contains(document.activeElement)) {
        var heading = document.querySelector("main h1");
        if (heading) {
          heading.setAttribute("tabindex", "-1");
          heading.focus({ preventScroll: true });
        }
      }
      slot.remove();
    };

    if (window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      finish();
      return;
    }
    slot.setAttribute("data-leaving", "true");
    slot.addEventListener("transitionend", function (ev) {
      // transitionend bubbles, so ignore anything from a descendant.
      if (ev.target !== slot) return;
      finish();
    });
    // Backstop: if the transition never fires (tab backgrounded, motion
    // preference flipped mid-flight) the row would otherwise be left collapsed
    // but still present, invisible to sight and not to a screen reader.
    window.setTimeout(finish, 400);
  });

})();
