/*
 * Alt8Mail Office — runtime branding, desktop-suite skin and editor defaults.
 *
 * The Office agent mounts this file over the image's
 * /usr/share/coolwsd/browser/dist/branding.js. coolwsd injects it into
 * cool.html after the app bundle; UIManager calls window.initializedUI() once
 * the UI is constructed.
 *
 * The skin is deliberately CSS/DOM-only so it ships with the agent and does not
 * require rebuilding the 2.8 GB Collabora image. It mirrors the compact
 * Office/ONLYOFFICE ribbon: small tabs, icon-first controls, a text style strip,
 * and narrow left/right rails. The document engine and WOPI pipeline are
 * untouched.
 */
(function () {
	'use strict';

	var PRODUCT_NAME = 'Alt8Mail Office';
	var LEGAL_ID = 'a8m-legal-notice';
	var STYLE_STRIP_ID = 'a8m-style-strip';
	var LEFT_RAIL_ID = 'a8m-left-rail';
	var RIGHT_RAIL_ID = 'a8m-right-rail';
	var started = false;
	var stripBuilt = false;
	var railsBuilt = false;
	var styleObserver = null;
	var sidebarObserver = null;
	var sidebarCloseUntil = 0;
	var rulerDone = false;
	var zoomDone = false;

	var STYLE_DEFS = [
		{ title: 'Default Paragraph Style', label: 'Normal' },
		{ title: 'No Spacing', label: 'No spacing' },
		{ title: 'Heading 1', label: 'Heading 1', level: 1 },
		{ title: 'Heading 2', label: 'Heading 2', level: 2 },
		{ title: 'Heading 3', label: 'Heading 3', level: 3 },
		{ title: 'Heading 4', label: 'Heading 4', level: 4 },
		{ title: 'Title', label: 'Title', titleStyle: true },
		{ title: 'Subtitle', label: 'Subtitle', titleStyle: true }
	];

	var ICONS = {
		search: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="10.5" cy="10.5" r="6.5"/><path d="m16 16 4.2 4.2"/></svg>',
		comment: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 5.5h16v11H9l-5 4v-15Z"/><path d="M8 9h8M8 12.5h5"/></svg>',
		navigator: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 6h12M4 12h9M4 18h12"/><circle cx="19" cy="6" r="1.4"/><circle cx="17" cy="12" r="1.4"/><circle cx="19" cy="18" r="1.4"/></svg>',
		properties: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M4 7h8M16 7h4M4 17h4M12 17h8"/><circle cx="14" cy="7" r="2.2"/><circle cx="10" cy="17" r="2.2"/></svg>',
		styles: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M5 18 11 6h2l6 12M8 14h8"/><path d="M4 21h16"/></svg>',
		accessibility: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="5" r="2"/><path d="M5 9h14M12 9v5M12 14l-4 7M12 14l4 7"/></svg>'
	};

	function addLegalNotice() {
		var info = document.getElementById('about-dialog-info');
		if (!info || document.getElementById(LEGAL_ID)) return;

		var note = document.createElement('p');
		note.id = LEGAL_ID;
		note.className = 'a8m-legal-notice';
		note.textContent =
			'Alt8Mail Office is built on Collabora Online and LibreOffice. ' +
			'Collabora Online is licensed under the Mozilla Public License 2.0. ' +
			'LibreOffice is licensed under the Mozilla Public License 2.0 and the GNU Lesser General Public License v3. ' +
			'Alt8Mail branding and modifications are provided by Alt8Mail.';
		info.appendChild(note);
	}

	function addSkinClass() {
		document.documentElement.classList.add('a8m-skin');
		if (document.body) document.body.classList.add('a8m-skin-body');
		try {
			if (window.parent && window.parent !== window.self) {
				document.documentElement.classList.add('a8m-embedded');
			}
		} catch (_error) {}
	}

	function decorateChrome() {
		var hiddenTabs = { Help: true, Table: true };
		var tabs = document.querySelectorAll('button.ui-tab.notebookbar');
		for (var i = 0; i < tabs.length; i += 1) {
			var tab = tabs[i];
			var text = (tab.textContent || '').trim();
			if (hiddenTabs[text]) tab.classList.add('a8m-tab-hidden');
		}

		var labels = document.querySelectorAll('.ui-overflow-group-label');
		for (var j = 0; j < labels.length; j += 1) {
			var label = labels[j];
			var name = (label.textContent || '').trim();
			if (name !== 'Insert' && name !== 'Search') continue;
			var group = label.closest('.ui-overflow-group');
			if (group) group.classList.add('a8m-home-hidden');
		}

		var inserts = document.querySelectorAll('#home-insert');
		for (var k = 0; k < inserts.length; k += 1) {
			inserts[k].classList.add('a8m-home-hidden');
		}
	}

	function styleEntryTitle(entry) {
		var img = entry.querySelector('img');
		if (img && img.title) return img.title.trim();
		var label = entry.querySelector('.ui-iconview-entry-title');
		return label ? (label.textContent || '').trim() : '';
	}

	function findStyleEntry(title) {
		var entries = document.querySelectorAll('#stylesview .ui-iconview-entry');
		for (var i = 0; i < entries.length; i += 1) {
			if (styleEntryTitle(entries[i]) === title) return entries[i];
		}
		return null;
	}

	function syncStyleStrip() {
		var strip = document.getElementById(STYLE_STRIP_ID);
		if (!strip) return;
		var selected = '';
		var entries = document.querySelectorAll('#stylesview .ui-iconview-entry');
		for (var i = 0; i < entries.length; i += 1) {
			if (entries[i].classList.contains('selected')) {
				selected = styleEntryTitle(entries[i]);
				break;
			}
		}
		var tiles = strip.querySelectorAll('.a8m-style-tile');
		for (var j = 0; j < tiles.length; j += 1) {
			tiles[j].classList.toggle('is-active', tiles[j].getAttribute('data-style-title') === selected);
		}
	}

	function buildStyleStrip() {
		if (!document.getElementById(STYLE_STRIP_ID)) stripBuilt = false;
		if (stripBuilt) {
			syncStyleStrip();
			return;
		}

		var gallery = document.getElementById('stylesview');
		var group = document.getElementById('home-stylesview-group');
		if (!gallery || !group || !group.parentNode) return;

		var strip = document.createElement('div');
		strip.id = STYLE_STRIP_ID;
		strip.className = 'a8m-style-strip';
		strip.setAttribute('role', 'group');
		strip.setAttribute('aria-label', 'Styles');

		var made = 0;
		for (var i = 0; i < STYLE_DEFS.length; i += 1) {
			var def = STYLE_DEFS[i];
			if (!findStyleEntry(def.title)) continue;

			var tile = document.createElement('button');
			tile.type = 'button';
			tile.className = 'a8m-style-tile';
			tile.textContent = def.label;
			tile.title = def.title;
			tile.setAttribute('data-style-title', def.title);
			if (def.level) tile.setAttribute('data-level', String(def.level));
			if (def.titleStyle) tile.setAttribute('data-title-style', '1');
			tile.addEventListener('click', function (title) {
				return function () {
					var entry = findStyleEntry(title);
					if (entry) {
						entry.click();
						window.setTimeout(syncStyleStrip, 80);
					}
				};
			}(def.title));
			strip.appendChild(tile);
			made += 1;
		}

		if (!made) return;
		group.parentNode.insertBefore(strip, group.nextSibling);
		stripBuilt = true;

		if (!styleObserver) {
			styleObserver = new MutationObserver(function () { syncStyleStrip(); });
			styleObserver.observe(gallery, { subtree: true, attributes: true, attributeFilter: ['class', 'aria-checked'] });
		}
		syncStyleStrip();
	}

	function railButton(label, icon, handler) {
		var button = document.createElement('button');
		button.type = 'button';
		button.className = 'a8m-rail-button';
		button.title = label;
		button.setAttribute('aria-label', label);
		button.innerHTML = ICONS[icon] || '';
		button.addEventListener('click', function (event) {
			event.preventDefault();
			event.stopPropagation();
			try { handler(); } catch (_error) {}
		});
		return button;
	}

	function buildRails() {
		if (railsBuilt && document.getElementById(LEFT_RAIL_ID) && document.getElementById(RIGHT_RAIL_ID)) return;
		var doc = document.getElementById('document-container');
		if (!doc || !doc.parentNode) return;
		var map = window.app && window.app.map;

		if (!document.getElementById(LEFT_RAIL_ID)) {
			var left = document.createElement('div');
			left.id = LEFT_RAIL_ID;
			left.className = 'a8m-rail a8m-rail-left';
			left.appendChild(railButton('Search', 'search', function () {
				if (map && typeof map.sendUnoCommand === 'function') map.sendUnoCommand('.uno:SearchDialog');
			}));
			left.appendChild(railButton('Comments', 'comment', function () {
				if (map && typeof map.sendUnoCommand === 'function') map.sendUnoCommand('.uno:ShowAnnotations');
			}));
			left.appendChild(railButton('Navigator', 'navigator', function () {
				if (map && typeof map.sendUnoCommand === 'function') map.sendUnoCommand('.uno:Navigator');
			}));
			doc.parentNode.insertBefore(left, doc);
		}

		if (!document.getElementById(RIGHT_RAIL_ID)) {
			var right = document.createElement('div');
			right.id = RIGHT_RAIL_ID;
			right.className = 'a8m-rail a8m-rail-right';
			right.appendChild(railButton('Properties', 'properties', function () {
				if (map && typeof map.sendUnoCommand === 'function') map.sendUnoCommand('.uno:SidebarDeck.PropertyDeck');
			}));
			right.appendChild(railButton('Styles', 'styles', function () {
				if (map && typeof map.sendUnoCommand === 'function') map.sendUnoCommand('.uno:SidebarDeck.StyleListDeck');
			}));
			right.appendChild(railButton('Accessibility', 'accessibility', function () {
				if (map && typeof map.sendUnoCommand === 'function') map.sendUnoCommand('.uno:SidebarDeck.A11yCheckDeck');
			}));
			doc.parentNode.appendChild(right);
		}

		var railGroups = [
			document.getElementById(LEFT_RAIL_ID),
			document.getElementById(RIGHT_RAIL_ID)
		];
		for (var r = 0; r < railGroups.length; r += 1) {
			if (!railGroups[r]) continue;
			(function (group) {
				var railButtons = group.querySelectorAll('.a8m-rail-button');
				for (var b = 0; b < railButtons.length; b += 1) {
					railButtons[b].addEventListener('click', function () {
						for (var n = 0; n < railButtons.length; n += 1) railButtons[n].classList.remove('is-active');
						this.classList.add('is-active');
					});
				}
			}(railGroups[r]));
		}

		railsBuilt = true;
		if (!sidebarObserver) {
			var sidebar = document.getElementById('sidebar-dock-wrapper');
			if (sidebar) {
				sidebarObserver = new MutationObserver(function () {
					var open = sidebar.classList.contains('visible');
					var buttons = document.querySelectorAll('#' + RIGHT_RAIL_ID + ' .a8m-rail-button');
					for (var i = 0; i < buttons.length; i += 1) {
						if (!open) buttons[i].classList.remove('is-active');
					}
				});
				sidebarObserver.observe(sidebar, { attributes: true, attributeFilter: ['class'] });
			}
		}

		window.setTimeout(function () {
			try { if (map && typeof map.invalidateSize === 'function') map.invalidateSize(); } catch (_error) {}
		}, 250);
	}

	function closeSidebar(map) {
		if (!sidebarCloseUntil) sidebarCloseUntil = Date.now() + 3000;
		if (Date.now() > sidebarCloseUntil) return;
		var docType = map.getDocType && map.getDocType();
		if (!docType) return;
		try {
			if (map.uiManager && typeof map.uiManager.setDocTypePref === 'function') {
				map.uiManager.setDocTypePref('ShowSidebar', false);
			}
			if (map.sidebar && typeof map.sidebar.closeSidebar === 'function') {
				map.sidebar.closeSidebar();
			}
		} catch (_error) {}
		window.setTimeout(function () {
			try { if (typeof map.invalidateSize === 'function') map.invalidateSize(); } catch (_error) {}
		}, 180);
	}

	function ensureRuler(map) {
		if (rulerDone) return;
		var ui = map.uiManager;
		if (!ui || typeof ui.showRuler !== 'function') return;
		try {
			if (typeof ui.setDocTypePref === 'function') ui.setDocTypePref('ShowRuler', true);
			ui.showRuler();
			if (typeof ui.isRulerVisible === 'function' && ui.isRulerVisible()) rulerDone = true;
		} catch (_error) {}
	}

	function ensureZoom(map) {
		if (zoomDone) return;
		if (!map._docLayer || typeof map.setZoom !== 'function') return;
		try {
			map.setZoom(10, null, false /* animate */);
			if (typeof map.getZoom !== 'function' || map.getZoom() === 10) zoomDone = true;
		} catch (_error) {}
	}

	function boot() {
		addSkinClass();
		addLegalNotice();
		decorateChrome();
		buildStyleStrip();
		buildRails();

		var map = window.app && window.app.map;
		if (!map) return;
		var docType = map.getDocType && map.getDocType();
		if (docType) closeSidebar(map);
		if (docType && map._docLayer) ensureRuler(map);
		ensureZoom(map);
	}

	function start() {
		if (started) return;
		started = true;
		addSkinClass();
		boot();
		var attempts = 0;
		var timer = window.setInterval(function () {
			attempts += 1;
			boot();
			if (attempts >= 80) window.clearInterval(timer);
		}, 250);
	}

	function brandDocument() {
		document.documentElement.classList.add('a8m-branded');
		addSkinClass();
		addLegalNotice();
		start();
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
