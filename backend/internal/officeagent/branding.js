/*
 * Alt8Mail Office — runtime branding and editor defaults.
 *
 * The Office agent mounts this file over the image's
 * /usr/share/coolwsd/browser/dist/branding.js. coolwsd injects it into
 * cool.html after the app bundle; UIManager calls window.initializedUI() once
 * the UI is constructed.
 */
(function () {
	'use strict';

	var PRODUCT_NAME = 'Alt8Mail Office';
	var LEGAL_ID = 'a8m-legal-notice';
	var SIDEBAR_KEY = 'a8m.sidebar-default';
	var RULER_KEY = 'a8m.ruler-default';
	var defaultsTimer = null;
	var zoomApplied = false;

	function addLegalNotice() {
		var info = document.getElementById('about-dialog-info');
		if (!info || document.getElementById(LEGAL_ID)) return;

		var note = document.createElement('p');
		note.id = LEGAL_ID;
		note.className = 'a8m-legal-notice';
		note.style.cssText = 'margin:10px 0 0;font-size:11px;line-height:1.45;opacity:.78;max-width:520px';
		note.textContent =
			'Alt8Mail Office is built on Collabora Online and LibreOffice. ' +
			'Collabora Online is licensed under the Mozilla Public License 2.0. ' +
			'LibreOffice is licensed under the Mozilla Public License 2.0 and the GNU Lesser General Public License v3. ' +
			'Alt8Mail branding and modifications are provided by Alt8Mail.';
		info.appendChild(note);
	}

	function currentDocType(map) {
		try {
			if (map && typeof map.getDocType === 'function') {
				return String(map.getDocType() || 'text');
			}
		} catch (_error) {}
		return 'text';
	}

	// OnlyOffice opens with the document canvas taking the full width. Collabora
	// defaults ShowSidebar to true per document type, which produces a wide
	// Styles panel on first use. Close it once, then respect whatever the user
	// chooses afterwards.
	function closeSidebarOnFirstRun(map) {
		try {
			if (localStorage.getItem(SIDEBAR_KEY)) return;
			var ui = map.uiManager;
			if (!ui || typeof ui.setDocTypePref !== 'function') return;

			ui.setDocTypePref('ShowSidebar', false);
			if (map.sidebar && typeof map.sidebar.isVisible === 'function' && map.sidebar.isVisible()) {
				map.sidebar.closeSidebar();
			}
			localStorage.setItem(SIDEBAR_KEY, '1');
		} catch (_error) {}
	}

	// Word and the other desktop suites open Writer with a ruler. Collabora
	// stores ShowRuler per document type, so apply the default once for each
	// type and leave the user's later choice alone.
	function showRulerOnFirstRun(map) {
		try {
			var key = RULER_KEY + '.' + currentDocType(map);
			if (localStorage.getItem(key)) return;

			var ui = map.uiManager;
			if (!ui || typeof ui.setDocTypePref !== 'function') return;

			ui.setDocTypePref('ShowRuler', true);
			if (typeof ui.isRulerVisible === 'function' &&
				!ui.isRulerVisible() &&
				typeof ui.showRuler === 'function') {
				ui.showRuler();
			}
			localStorage.setItem(key, '1');
		} catch (_error) {}
	}

	// Collabora's status bar scale is 10 for 100%. Apply it once the document
	// layer exists; a later zoom change by the user is not overridden.
	function setDefaultZoom(map) {
		if (zoomApplied) return;
		try {
			if (!map || !map._docLayer || typeof map.setZoom !== 'function') return;
			map.setZoom(10, null, false /* animate */);
			zoomApplied = true;
		} catch (_error) {}
	}

	function applyEditorDefaults() {
		var map = window.app && window.app.map;
		if (!map) return;
		closeSidebarOnFirstRun(map);
		showRulerOnFirstRun(map);
		setDefaultZoom(map);
	}

	function startEditorDefaults() {
		if (defaultsTimer) return;
		applyEditorDefaults();
		var attempts = 0;
		defaultsTimer = setInterval(function () {
			attempts++;
			applyEditorDefaults();
			// Keep trying for fifteen seconds: the document layer and the
			// per-document UI preferences are not ready when initializedUI runs.
			if (attempts >= 30) {
				clearInterval(defaultsTimer);
				defaultsTimer = null;
			}
		}, 500);
	}

	function brandDocument() {
		document.title = PRODUCT_NAME;
		document.documentElement.classList.add('a8m-branded');
		addLegalNotice();
		startEditorDefaults();
	}

	window.initializedUI = function () {
		brandDocument();
	};

	if (document.readyState === 'loading') {
		document.addEventListener('DOMContentLoaded', brandDocument);
	} else {
		brandDocument();
	}
})();
