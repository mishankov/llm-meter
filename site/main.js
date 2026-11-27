(function () {
  "use strict";

  var THEME_KEY = "llm-meter-site-theme";
  var root = document.documentElement;
  var announcer = document.getElementById("live-announcer");

  function announce(message) {
    if (!announcer) return;
    announcer.textContent = "";
    window.requestAnimationFrame(function () {
      announcer.textContent = message;
    });
  }

  // 1. Theme state (respects prefers-color-scheme + interactive toggle + dashboard image swap)
  var themeButtons = document.querySelectorAll("[data-set-theme]");
  var dashboardImg = document.getElementById("dashboard-screenshot");
  var stageLabel = document.getElementById("stage-theme-label");
  var stageToggle = document.getElementById("stage-theme-toggle");

  function applyTheme(theme, persist) {
    var resolved = theme === "light" ? "light" : "dark";
    root.setAttribute("data-theme", resolved);

    if (persist) {
      try {
        localStorage.setItem(THEME_KEY, resolved);
      } catch (e) {
        // Ignore storage restrictions
      }
    }

    themeButtons.forEach(function (btn) {
      var isActive = btn.getAttribute("data-set-theme") === resolved;
      btn.setAttribute("aria-pressed", isActive ? "true" : "false");
    });

    if (dashboardImg) {
      var nextSrc = resolved === "light"
        ? dashboardImg.getAttribute("data-light-src")
        : dashboardImg.getAttribute("data-dark-src");
      if (nextSrc && dashboardImg.getAttribute("src") !== nextSrc) {
        dashboardImg.setAttribute("src", nextSrc);
      }
    }

    if (stageLabel) {
      stageLabel.textContent = resolved === "light"
        ? "Showing light dashboard"
        : "Showing dark dashboard";
    }

    if (stageToggle) {
      stageToggle.textContent = resolved === "light"
        ? "Switch to dark view"
        : "Switch to light view";
    }
  }

  var initialTheme = root.getAttribute("data-theme") || "dark";
  applyTheme(initialTheme, false);

  themeButtons.forEach(function (btn) {
    btn.addEventListener("click", function () {
      var next = btn.getAttribute("data-set-theme");
      applyTheme(next, true);
      announce("Switched to " + next + " theme");
    });
  });

  if (stageToggle) {
    stageToggle.addEventListener("click", function () {
      var current = root.getAttribute("data-theme") === "light" ? "light" : "dark";
      var next = current === "light" ? "dark" : "light";
      applyTheme(next, true);
      announce("Switched to " + next + " theme");
    });
  }

  if (window.matchMedia) {
    var mediaQuery = window.matchMedia("(prefers-color-scheme: light)");
    var handleSystemChange = function (event) {
      var hasExplicitChoice = false;
      try {
        var stored = localStorage.getItem(THEME_KEY);
        hasExplicitChoice = stored === "light" || stored === "dark";
      } catch (e) {
        hasExplicitChoice = false;
      }
      if (!hasExplicitChoice) {
        applyTheme(event.matches ? "light" : "dark", false);
      }
    };
    if (typeof mediaQuery.addEventListener === "function") {
      mediaQuery.addEventListener("change", handleSystemChange);
    }
  }

  // Helper: WAI-ARIA arrow-key navigation for tablists
  function bindTablistKeyboard(tablist) {
    var tabs = Array.prototype.slice.call(tablist.querySelectorAll('[role="tab"]'));
    tabs.forEach(function (tab, index) {
      tab.addEventListener("keydown", function (event) {
        var nextIndex = null;
        if (event.key === "ArrowRight") nextIndex = (index + 1) % tabs.length;
        if (event.key === "ArrowLeft") nextIndex = (index - 1 + tabs.length) % tabs.length;
        if (event.key === "Home") nextIndex = 0;
        if (event.key === "End") nextIndex = tabs.length - 1;
        if (nextIndex !== null) {
          event.preventDefault();
          tabs[nextIndex].focus();
          tabs[nextIndex].click();
        }
      });
    });
  }

  document.querySelectorAll('[role="tablist"]').forEach(bindTablistKeyboard);

  // 2. Quickstart snippet tabs
  document.querySelectorAll("[data-snippet-group]").forEach(function (tab) {
    tab.addEventListener("click", function () {
      var group = tab.getAttribute("data-snippet-group");
      var snippet = tab.getAttribute("data-snippet");

      document.querySelectorAll('[data-snippet-group="' + group + '"]').forEach(function (t) {
        t.setAttribute("aria-selected", t === tab ? "true" : "false");
      });

      document.querySelectorAll('[data-panel-group="' + group + '"]').forEach(function (panel) {
        panel.hidden = panel.getAttribute("data-panel") !== snippet;
      });
    });
  });

  // 5. Copy-to-clipboard buttons
  function copyText(text) {
    if (navigator.clipboard && typeof navigator.clipboard.writeText === "function") {
      return navigator.clipboard.writeText(text);
    }
    return new Promise(function (resolve, reject) {
      var ta = document.createElement("textarea");
      ta.value = text;
      ta.setAttribute("readonly", "");
      ta.style.position = "fixed";
      ta.style.opacity = "0";
      document.body.appendChild(ta);
      ta.select();
      try {
        var ok = document.execCommand("copy");
        document.body.removeChild(ta);
        if (ok) resolve(); else reject(new Error("copy failed"));
      } catch (err) {
        document.body.removeChild(ta);
        reject(err);
      }
    });
  }

  document.querySelectorAll(".copy-btn").forEach(function (btn) {
    var resetTimer = null;
    btn.addEventListener("click", function () {
      var raw = btn.getAttribute("data-copy");
      var targetId = btn.getAttribute("data-copy-target");
      var text = raw;
      if (!text && targetId) {
        var codeEl = document.getElementById(targetId);
        text = codeEl ? codeEl.textContent : "";
      }
      if (!text) return;

      var label = btn.querySelector(".copy-label");
      copyText(text).then(function () {
        btn.setAttribute("data-copied", "true");
        if (label) label.textContent = "Copied";
        announce("Copied command to clipboard");
        window.clearTimeout(resetTimer);
        resetTimer = window.setTimeout(function () {
          btn.removeAttribute("data-copied");
          if (label) label.textContent = "Copy";
        }, 1800);
      }).catch(function () {
        if (label) label.textContent = "Failed";
        announce("Could not copy to clipboard");
      });
    });
  });
})();
