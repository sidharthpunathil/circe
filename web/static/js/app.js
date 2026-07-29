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
  function countSelected(scope) {
    var boxes = scope.querySelectorAll('input[type="checkbox"]');
    var n = 0;
    boxes.forEach(function (b) { if (b.checked) n++; });
    document.querySelectorAll("[data-selected-count]").forEach(function (el) {
      el.textContent = String(n);
    });
  }

  document.querySelectorAll("[data-filter]").forEach(function (input) {
    var scope = document.querySelector(input.getAttribute("data-filter"));
    if (!scope) return;
    var emptyMsg = document.querySelector("[data-filter-empty]");
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

  document.querySelectorAll("#skill-picker, #import-picker").forEach(function (scope) {
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
})();
