(function () {
  "use strict";

  var THEME_KEY = "llm-meter-site-theme";
  var root = document.documentElement;
  var announcer = document.getElementById("live-announcer");
  var reduceMotionQuery = window.matchMedia ? window.matchMedia("(prefers-reduced-motion: reduce)") : null;

  function prefersReducedMotion() {
    return !!(reduceMotionQuery && reduceMotionQuery.matches);
  }

  function announce(message) {
    if (!announcer) return;
    announcer.textContent = "";
    window.requestAnimationFrame(function () {
      announcer.textContent = message;
    });
  }

  // 1. Sliding pill indicators for segmented controls & theme switch
  var slidingGroups = Array.prototype.slice.call(document.querySelectorAll(".sliding-group"));

  function updateSlidingPill(group, immediate) {
    if (!group) return;
    var pill = group.querySelector(".sliding-pill");
    if (!pill) return;
    var active = group.querySelector('[aria-selected="true"], [aria-pressed="true"]');
    if (!active) return;

    var left = active.offsetLeft;
    var width = active.offsetWidth;
    if (width <= 0) return;

    if (immediate) {
      pill.style.transition = "none";
    }
    pill.style.width = width + "px";
    pill.style.transform = "translateX(" + left + "px)";
    group.classList.add("has-pill");
    if (immediate) {
      // Force reflow then restore transition
      void pill.offsetWidth;
      pill.style.transition = "";
    }
  }

  function updateAllPills(immediate) {
    slidingGroups.forEach(function (group) {
      updateSlidingPill(group, immediate);
    });
  }

  // 2. Theme + Dashboard stage view mode (Dark / Split / Light)
  var themeButtons = document.querySelectorAll("[data-set-theme]");
  var stageEl = document.getElementById("dashboard-stage");
  var stageModeButtons = document.querySelectorAll("[data-stage-mode]");
  var splitDivider = document.getElementById("split-divider");
  var dashboardFrame = document.getElementById("dashboard-frame");
  var currentStageMode = root.getAttribute("data-theme") === "light" ? "light" : "dark";
  var splitPercent = 50;

  function setStageMode(mode, syncTheme) {
    currentStageMode = mode === "split" || mode === "light" ? mode : "dark";
    if (stageEl) {
      stageEl.setAttribute("data-view-mode", currentStageMode);
    }
    if (splitDivider) {
      splitDivider.hidden = currentStageMode !== "split";
    }
    stageModeButtons.forEach(function (btn) {
      var active = btn.getAttribute("data-stage-mode") === currentStageMode;
      btn.setAttribute("aria-selected", active ? "true" : "false");
    });

    var stageGroup = stageEl ? stageEl.querySelector(".stage-mode-switch") : null;
    updateSlidingPill(stageGroup, false);

    if (syncTheme && (currentStageMode === "dark" || currentStageMode === "light")) {
      applyTheme(currentStageMode, true, null);
    }
  }

  function commitThemeDOM(resolved, persist) {
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

    var headerSwitch = document.querySelector(".theme-switch");
    updateSlidingPill(headerSwitch, false);

    if (currentStageMode !== "split") {
      setStageMode(resolved, false);
    }
  }

  function applyTheme(theme, persist, originEvent) {
    var resolved = theme === "light" ? "light" : "dark";
    var current = root.getAttribute("data-theme");
    if (current === resolved) {
      commitThemeDOM(resolved, persist);
      return;
    }

    var canTransition =
      originEvent &&
      typeof document.startViewTransition === "function" &&
      !prefersReducedMotion();

    if (canTransition) {
      var x = window.innerWidth * 0.5;
      var y = 32;
      if (originEvent.clientX || originEvent.clientY) {
        x = originEvent.clientX;
        y = originEvent.clientY;
      } else if (originEvent.currentTarget && originEvent.currentTarget.getBoundingClientRect) {
        var rect = originEvent.currentTarget.getBoundingClientRect();
        x = rect.left + rect.width * 0.5;
        y = rect.top + rect.height * 0.5;
      }
      var maxDist = Math.hypot(
        Math.max(x, window.innerWidth - x),
        Math.max(y, window.innerHeight - y)
      );
      root.style.setProperty("--vt-x", x + "px");
      root.style.setProperty("--vt-y", y + "px");
      root.style.setProperty("--vt-r", Math.ceil(maxDist) + "px");

      document.startViewTransition(function () {
        commitThemeDOM(resolved, persist);
      });
    } else {
      commitThemeDOM(resolved, persist);
    }
  }

  var initialTheme = root.getAttribute("data-theme") || "dark";
  var urlMode = null;
  try {
    urlMode = new URLSearchParams(window.location.search).get("mode");
  } catch (e) {
    urlMode = null;
  }
  commitThemeDOM(initialTheme, false);
  setStageMode(urlMode === "split" || urlMode === "light" || urlMode === "dark" ? urlMode : initialTheme, false);

  themeButtons.forEach(function (btn) {
    btn.addEventListener("click", function (event) {
      var next = btn.getAttribute("data-set-theme");
      if (currentStageMode === "split") {
        currentStageMode = next;
      }
      applyTheme(next, true, event);
      setStageMode(next, false);
      announce("Switched to " + next + " theme");
    });
  });

  stageModeButtons.forEach(function (btn) {
    btn.addEventListener("click", function (event) {
      var mode = btn.getAttribute("data-stage-mode");
      if (mode === "split") {
        setStageMode("split", false);
        announce("Comparing dark and light dashboards side by side");
      } else {
        setStageMode(mode, false);
        applyTheme(mode, true, event);
        announce("Switched to " + mode + " dashboard and theme");
      }
    });
  });

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
        applyTheme(event.matches ? "light" : "dark", false, null);
      }
    };
    if (typeof mediaQuery.addEventListener === "function") {
      mediaQuery.addEventListener("change", handleSystemChange);
    }
  }

  // 3. Interactive Split Comparison Slider (Dark <-> Light)
  function setSplitPercent(pct) {
    splitPercent = Math.max(8, Math.min(92, Math.round(pct * 10) / 10));
    if (dashboardFrame) {
      dashboardFrame.style.setProperty("--split", splitPercent + "%");
    }
    if (splitDivider) {
      var rounded = Math.round(splitPercent);
      splitDivider.setAttribute("aria-valuenow", String(rounded));
      splitDivider.setAttribute("aria-valuetext", rounded + "% dark, " + (100 - rounded) + "% light");
    }
  }

  if (dashboardFrame && splitDivider && stageEl) {
    var draggingSplit = false;

    function updateSplitFromClientX(clientX) {
      var rect = dashboardFrame.getBoundingClientRect();
      if (rect.width <= 0) return;
      var pct = ((clientX - rect.left) / rect.width) * 100;
      setSplitPercent(pct);
    }

    dashboardFrame.addEventListener("pointerdown", function (event) {
      if (currentStageMode !== "split") return;
      draggingSplit = true;
      stageEl.classList.add("is-dragging");
      if (dashboardFrame.setPointerCapture) {
        try { dashboardFrame.setPointerCapture(event.pointerId); } catch (e) {}
      }
      updateSplitFromClientX(event.clientX);
    });

    dashboardFrame.addEventListener("pointermove", function (event) {
      if (!draggingSplit) return;
      updateSplitFromClientX(event.clientX);
    });

    function endSplitDrag(event) {
      if (!draggingSplit) return;
      draggingSplit = false;
      stageEl.classList.remove("is-dragging");
      if (dashboardFrame.releasePointerCapture && event && event.pointerId != null) {
        try { dashboardFrame.releasePointerCapture(event.pointerId); } catch (e) {}
      }
    }

    dashboardFrame.addEventListener("pointerup", endSplitDrag);
    dashboardFrame.addEventListener("pointercancel", endSplitDrag);

    splitDivider.addEventListener("keydown", function (event) {
      var step = event.shiftKey ? 10 : 4;
      if (event.key === "ArrowLeft") {
        event.preventDefault();
        setSplitPercent(splitPercent - step);
      } else if (event.key === "ArrowRight") {
        event.preventDefault();
        setSplitPercent(splitPercent + step);
      } else if (event.key === "Home") {
        event.preventDefault();
        setSplitPercent(8);
      } else if (event.key === "End") {
        event.preventDefault();
        setSplitPercent(92);
      }
    });
  }

  // 4. Token-mix legend interactive highlight
  var stageLegend = document.querySelector(".stage-legend");
  if (stageLegend) {
    stageLegend.querySelectorAll("[data-mix-hover]").forEach(function (item) {
      var mixKey = item.getAttribute("data-mix-hover");
      item.addEventListener("mouseenter", function () {
        stageLegend.setAttribute("data-highlight", mixKey);
      });
      item.addEventListener("mouseleave", function () {
        stageLegend.removeAttribute("data-highlight");
      });
    });
  }

  // 5. Subtle damped 3D perspective tilt + specular sheen on Dashboard Stage
  if (stageEl) {
    var tiltRaf = null;
    var targetRx = 0;
    var targetRy = 0;
    var currentRx = 0;
    var currentRy = 0;

    function animateTilt() {
      currentRx += (targetRx - currentRx) * 0.14;
      currentRy += (targetRy - currentRy) * 0.14;
      stageEl.style.transform =
        "rotateX(" + currentRx.toFixed(3) + "deg) rotateY(" + currentRy.toFixed(3) + "deg)";

      if (Math.abs(targetRx - currentRx) > 0.008 || Math.abs(targetRy - currentRy) > 0.008) {
        tiltRaf = window.requestAnimationFrame(animateTilt);
      } else {
        if (targetRx === 0 && targetRy === 0) {
          stageEl.style.transform = "";
        }
        tiltRaf = null;
      }
    }

    function scheduleTilt() {
      if (!tiltRaf && !prefersReducedMotion()) {
        tiltRaf = window.requestAnimationFrame(animateTilt);
      }
    }

    stageEl.addEventListener("pointermove", function (event) {
      if (prefersReducedMotion() || event.pointerType === "touch") return;
      var rect = stageEl.getBoundingClientRect();
      if (rect.width <= 0 || rect.height <= 0) return;
      var nx = (event.clientX - rect.left) / rect.width;
      var ny = (event.clientY - rect.top) / rect.height;

      stageEl.style.setProperty("--sx", (nx * 100).toFixed(1) + "%");
      stageEl.style.setProperty("--sy", (ny * 100).toFixed(1) + "%");

      // Subtle executive perspective tilt (max ±1.35deg so dashboard text stays razor sharp)
      targetRy = (nx - 0.5) * 2.4;
      targetRx = (0.5 - ny) * 1.9;
      scheduleTilt();
    });

    stageEl.addEventListener("pointerleave", function () {
      targetRx = 0;
      targetRy = 0;
      scheduleTilt();
    });
  }

  // 6. Pointer-tracking luminous borders on .glow-panel cards
  document.querySelectorAll(".glow-panel").forEach(function (panel) {
    panel.addEventListener("pointermove", function (event) {
      if (event.pointerType === "touch") return;
      var rect = panel.getBoundingClientRect();
      panel.style.setProperty("--mx", (event.clientX - rect.left).toFixed(1) + "px");
      panel.style.setProperty("--my", (event.clientY - rect.top).toFixed(1) + "px");
    });
  });

  // 7. Header scroll state & telemetry progress bar
  var siteHeader = document.getElementById("site-header");
  var scrollFill = document.getElementById("scroll-meter-fill");
  var scrollTicking = false;

  function updateScrollProgress() {
    scrollTicking = false;
    var y = window.scrollY || window.pageYOffset || 0;
    if (siteHeader) {
      siteHeader.classList.toggle("is-scrolled", y > 12);
    }
    if (scrollFill) {
      var docHeight = document.documentElement.scrollHeight - window.innerHeight;
      var ratio = docHeight > 0 ? Math.max(0, Math.min(1, y / docHeight)) : 0;
      scrollFill.style.transform = "scaleX(" + ratio.toFixed(4) + ")";
    }
  }

  window.addEventListener("scroll", function () {
    if (!scrollTicking) {
      scrollTicking = true;
      window.requestAnimationFrame(updateScrollProgress);
    }
  }, { passive: true });
  updateScrollProgress();

  // 8. Reposition sliding pills on viewport resize
  var resizeTimer = null;
  window.addEventListener("resize", function () {
    window.clearTimeout(resizeTimer);
    resizeTimer = window.setTimeout(function () {
      updateAllPills(true);
    }, 120);
  });

  // 9. WAI-ARIA arrow-key navigation for tablists
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

  // 10. Quickstart snippet tabs
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

      var parentGroup = tab.closest(".sliding-group");
      updateSlidingPill(parentGroup, false);
    });
  });

  // 11. Copy-to-clipboard buttons
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

  // Position all sliding pills once fonts & layout settle
  updateAllPills(true);
  if (document.fonts && document.fonts.ready) {
    document.fonts.ready.then(function () {
      updateAllPills(true);
    });
  }
})();
