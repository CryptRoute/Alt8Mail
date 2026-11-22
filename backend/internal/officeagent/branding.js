/*
 * Alt8Mail Office — runtime branding.
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

	// OnlyOffice opens with the document canvas taking the full width. Collabora
	// defaults ShowSidebar to true per document type, which produces a wide
	// Styles panel on first use. Close it once, then respect whatever the user
	// chooses afterwards.
	function closeSidebarOnFirstRun() {
		try {
			if (localStorage.getItem(SIDEBAR_KEY)) return;
			localStorage.setItem(SIDEBAR_KEY, '1');

			var apply = function () {
				try {
					var map = window.app && window.app.map;
					if (!map) return;
					var ui = map.uiManager;
					if (ui && typeof ui.setDocTypePref === 'function') {
						ui.setDocTypePref('ShowSidebar', false);
					}
					if (map.sidebar && typeof map.sidebar.isVisible === 'function' && map.sidebar.isVisible()) {
						map.sidebar.closeSidebar();
					}
				} catch (_error) {}
			};

			apply();
			var attempts = 0;
			var timer = setInterval(function () {
				attempts++;
				apply();
				if (attempts >= 8) clearInterval(timer);
			}, 500);
		} catch (_error) {}
	}

	function brandDocument() {
		document.title = PRODUCT_NAME;
		document.documentElement.classList.add('a8m-branded');
		addLegalNotice();
		closeSidebarOnFirstRun();
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
