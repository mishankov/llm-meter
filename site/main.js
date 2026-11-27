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

  // 2. Supported clients table: Direct vs Local Relay endpoint switch
  var clientModeButtons = document.querySelectorAll("[data-client-mode]");
  var endpointColHeading = document.getElementById("endpoint-col-heading");
  var clientsPanel = document.getElementById("clients-table-panel");

  clientModeButtons.forEach(function (btn) {
    btn.addEventListener("click", function () {
      var mode = btn.getAttribute("data-client-mode");
      clientModeButtons.forEach(function (b) {
        b.setAttribute("aria-selected", b === btn ? "true" : "false");
      });
      if (clientsPanel && btn.id) {
        clientsPanel.setAttribute("aria-labelledby", btn.id);
      }
      if (endpointColHeading) {
        endpointColHeading.textContent = mode === "relay"
          ? "Local relay endpoint"
          : "Direct server endpoint";
      }
      document.querySelectorAll(".endpoint-code").forEach(function (el) {
        el.textContent = el.getAttribute("data-" + mode) || "";
      });
      document.querySelectorAll(".auth-cell").forEach(function (el) {
        el.textContent = el.getAttribute("data-" + mode) || "";
      });
    });
  });

  // 3. Architecture interactive topology selector
  var topoData = {
    direct: {
      heading: "Direct to server — zero extra process",
      copyHtml: "Export OpenTelemetry directly to <code>&lt;server&gt;/otlp</code> or proxy OpenAI-compatible and Antigravity traffic through <code>/v1</code> and <code>/antigravity</code> with <code>X-LLM-Meter-Key</code>.",
      traffic: "OTLP clients hit providers directly; direct proxy routes forward through LLM Meter.",
      visibilityHtml: "Usage metadata for OTLP (content discarded); proxied requests for <code>/v1</code> and <code>/antigravity</code>.",
      best: "Local deployments or single-CLI setups where you want no background relay process."
    },
    relay: {
      heading: "Local relay — usage-only & local OTLP",
      copyHtml: "Run one local relay (Go binary, Docker, or macOS menu bar app) to authenticate every tool from one key, strip prompts on <code>127.0.0.1</code>, and queue usage in SQLite across outages.",
      traffic: "Both coding CLIs and usage-only proxy clients send model requests directly to providers.",
      visibilityHtml: "Filtered usage metadata only via <code>POST /api/usage/events</code>—prompts and provider keys stay local.",
      best: "Workstations running multiple AI CLIs, strict prompt privacy, or unreliable network links."
    },
    remote: {
      heading: "Local relay — remote-proxy mode",
      copyHtml: "Keep endpoint-only clients (such as Antigravity ACP) on localhost while the relay injects <code>X-LLM-Meter-Key</code> and routes model traffic through your central LLM Meter server.",
      traffic: "Client → Local Relay → LLM Meter server → Model Provider over HTTPS.",
      visibilityHtml: "Full proxied requests and upstream auth headers, with <code>X-LLM-Meter-Key</code> stripped upstream.",
      best: "Centralized routing for endpoint-only proxy clients without configuring headers per tool."
    }
  };

  var topoWorkbench = document.getElementById("topo-panel");
  var topoHeading = document.getElementById("topo-heading");
  var topoCopy = document.getElementById("topo-copy");
  var topoTraffic = document.getElementById("topo-traffic");
  var topoVisibility = document.getElementById("topo-visibility");
  var topoBest = document.getElementById("topo-best");
  var topoTabs = document.querySelectorAll("[data-topo]");

  topoTabs.forEach(function (tab) {
    tab.addEventListener("click", function () {
      var mode = tab.getAttribute("data-topo");
      var info = topoData[mode];
      if (!info) return;

      topoTabs.forEach(function (t) {
        t.setAttribute("aria-selected", t === tab ? "true" : "false");
      });

      if (topoWorkbench) {
        topoWorkbench.setAttribute("data-active-topo", mode);
        if (tab.id) topoWorkbench.setAttribute("aria-labelledby", tab.id);
      }
      if (topoHeading) topoHeading.textContent = info.heading;
      if (topoCopy) topoCopy.innerHTML = info.copyHtml;
      if (topoTraffic) topoTraffic.textContent = info.traffic;
      if (topoVisibility) topoVisibility.innerHTML = info.visibilityHtml;
      if (topoBest) topoBest.textContent = info.best;
    });
  });

  // 4. Quickstart snippet tabs
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
