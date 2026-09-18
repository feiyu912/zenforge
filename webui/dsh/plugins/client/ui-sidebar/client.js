window.__ModuleLoader__.load({
	id: "@deepseek-ai/dsh-client-ui-sidebar",
	factory: (require) => {
		var module = { exports: {} };
		var exports = module.exports;
		Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
		let _deepseek_ai_dsh_client_store = require("@deepseek-ai/dsh-client-store");
		let _deepseek_ai_dsh_client_ui_slots = require("@deepseek-ai/dsh-client-ui-slots");
		let react_jsx_runtime = require("react/jsx-runtime");
		let _deepseek_ai_dsh_client_ui_primitives = require("@deepseek-ai/dsh-client-ui-primitives");
		let react = require("react");
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-sidebar/src/client/HeaderLeadingControls.module.css.mjs
		const css$1 = ".Vc4HpW_controls{display:none}[data-sidebar-collapsed] .Vc4HpW_controls{align-items:center;gap:8px;margin-right:8px;padding-left:68px;display:flex}.Vc4HpW_iconButton{corner-shape:round;width:28px;height:28px;color:var(--dsw-alias-label-secondary);cursor:pointer;background:0 0;border:none;border-radius:50%;flex:none;justify-content:center;align-items:center;padding:0;display:inline-flex}.Vc4HpW_iconButton:hover{background:var(--dsw-alias-interactive-bg-hover)}.Vc4HpW_iconButton svg{width:15px;height:15px}";
		const tagId$1 = "@deepseek-ai/dsh-client-ui-sidebar/HeaderLeadingControls.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$1) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-sidebar";
			tag.dataset.pluginCss = tagId$1;
			tag.textContent = css$1;
			document.head.appendChild(tag);
		}
		var HeaderLeadingControls_module_css_default = {
			"controls": "Vc4HpW_controls",
			"iconButton": "Vc4HpW_iconButton"
		};
		//#endregion
		//#region lib/types/client/HeaderLeadingControls.js
		/** macOS-desktop conversation-header controls for the fully hidden sidebar. */
		/**
		* Sidebar-open and New Session controls in the conversation header's leading
		* seat. On macOS desktop a collapsed sidebar hides entirely (no rail), taking
		* both controls off screen; this occupant puts them back beside the traffic
		* lights. Mounted whenever the platform matches; visibility rides the
		* AppFrame-published `data-sidebar-collapsed` attribute in CSS, so no
		* collapse-state pipe is added here.
		* @param props - Injected sidebar actions plus the sidebar locale seat.
		* @returns the two header controls, or null off macOS desktop.
		*/
		function HeaderLeadingControls({ toggleSidebar, startSession, t }) {
			if (!(0, _deepseek_ai_dsh_client_ui_primitives.isDarwinDesktop)()) return null;
			return (0, react_jsx_runtime.jsxs)("div", {
				className: HeaderLeadingControls_module_css_default.controls,
				children: [(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tooltip, {
					label: t("toggle.open"),
					delayMs: 500,
					children: (0, react_jsx_runtime.jsx)("button", {
						type: "button",
						className: HeaderLeadingControls_module_css_default.iconButton,
						"aria-label": t("toggle.open"),
						onClick: () => {
							toggleSidebar();
						},
						children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconPanelLeftOutline16, { size: 16 })
					})
				}), (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tooltip, {
					label: t("session.new.label"),
					delayMs: 500,
					children: (0, react_jsx_runtime.jsx)("button", {
						type: "button",
						className: HeaderLeadingControls_module_css_default.iconButton,
						"aria-label": t("session.new.label"),
						onClick: () => {
							startSession();
						},
						children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconNewChatOutline16, { size: 16 })
					})
				})]
			});
		}
		//#endregion
		//#region ../../../node_modules/.pnpm/clsx@2.1.1/node_modules/clsx/dist/clsx.mjs
		function r(e) {
			var t, f, n = "";
			if ("string" == typeof e || "number" == typeof e) n += e;
			else if ("object" == typeof e) if (Array.isArray(e)) {
				var o = e.length;
				for (t = 0; t < o; t++) e[t] && (f = r(e[t])) && (n && (n += " "), n += f);
			} else for (f in e) e[f] && (n && (n += " "), n += f);
			return n;
		}
		function clsx() {
			for (var e, t, f = 0, n = "", o = arguments.length; f < o; f++) (e = arguments[f]) && (t = r(e)) && (n && (n += " "), n += t);
			return n;
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-sidebar/src/client/SidebarRoot.module.css.mjs
		const css = "._49Vfmq_root{--dsh-sidebar-inline-padding:12px;height:100%;padding:6px var(--dsh-sidebar-inline-padding);box-sizing:border-box;background:var(--dsw-specific-sidebar-fill);color:var(--dsw-alias-label-primary);--dsh-scrollbar-thumb:var(--dsw-alias-scrollbar-bg-l2);--dsh-scrollbar-thumb-hover:var(--dsw-alias-scrollbar-hover-l2);flex-direction:column;font-size:14px;display:flex}[data-platform=darwin] ._49Vfmq_root{background:0 0}._49Vfmq_root._49Vfmq_collapsed{padding:18px 10px 6px}[data-windows-titlebar] ._49Vfmq_root ._49Vfmq_logoRow{height:40px;margin:0;padding:0}[data-windows-titlebar] ._49Vfmq_toggle{top:calc((var(--dsh-windows-titlebar-height) - 28px) / 2);z-index:30;-webkit-app-region:no-drag;position:fixed;left:12px}[data-windows-titlebar] ._49Vfmq_root._49Vfmq_collapsed ._49Vfmq_logoRow{height:0}[data-windows-titlebar] ._49Vfmq_root:not(._49Vfmq_collapsed) ._49Vfmq_brand{padding-left:4px}[data-windows-titlebar] ._49Vfmq_brandIdentity{transform:translateY(1px)}[data-windows-titlebar] ._49Vfmq_brandMark{transform:translate(1px)}[data-windows-titlebar] ._49Vfmq_brandName{font-weight:400}[data-windows-titlebar] ._49Vfmq_root._49Vfmq_collapsed{padding:0}[data-windows-titlebar] ._49Vfmq_root:not(._49Vfmq_collapsed) ._49Vfmq_newSession{margin-top:8px;margin-left:0}[data-windows-titlebar] ._49Vfmq_collapsed ._49Vfmq_panelList,[data-windows-titlebar] ._49Vfmq_collapsed ._49Vfmq_regionArea,[data-windows-titlebar] ._49Vfmq_collapsed ._49Vfmq_footArea{display:none}html[data-windows-titlebar]:has([data-sidebar-collapsed=true]){--dsh-windows-menu-start:84px}[data-windows-titlebar] ._49Vfmq_collapsed ._49Vfmq_newSession{top:calc((var(--dsh-windows-titlebar-height) - 28px) / 2);z-index:30;-webkit-app-region:no-drag;border:none;margin:0;padding:0;position:fixed;left:48px}[data-windows-titlebar] ._49Vfmq_railIn ._49Vfmq_iconButton,[data-windows-titlebar] ._49Vfmq_railIn ._49Vfmq_newSession{animation:none}[data-windows-titlebar] ._49Vfmq_collapsed ._49Vfmq_toggle,[data-windows-titlebar] ._49Vfmq_collapsed ._49Vfmq_newSession{corner-shape:round;width:28px;height:28px;color:var(--dsw-alias-label-secondary);border-radius:50%}[data-windows-titlebar] ._49Vfmq_collapsed ._49Vfmq_toggle ._49Vfmq_panelIcon{display:inline}._49Vfmq_root._49Vfmq_quietBars{--dsh-scrollbar-thumb:transparent;--dsh-scrollbar-thumb-hover:transparent}._49Vfmq_fading>*{opacity:0;transition:opacity .15s var(--ds-ease-in-out)}._49Vfmq_wide{animation:_49Vfmq_wide-in .2s var(--ds-ease-in-out)}@keyframes _49Vfmq_wide-in{0%{opacity:0}}._49Vfmq_railIn ._49Vfmq_iconButton,._49Vfmq_railIn ._49Vfmq_newSession,._49Vfmq_railIn ._49Vfmq_panelList,._49Vfmq_railIn ._49Vfmq_regionArea{animation:_49Vfmq_rail-in .15s var(--ds-ease-in-out) backwards}._49Vfmq_railIn ._49Vfmq_footArea{animation:_49Vfmq_rail-fade-in .15s var(--ds-ease-in-out) backwards}@keyframes _49Vfmq_rail-in{0%{opacity:0;transform:translate(49px)}}@keyframes _49Vfmq_rail-fade-in{0%{opacity:0}}._49Vfmq_topStrip{box-sizing:border-box;height:52px;margin:-6px calc(-1 * var(--dsh-sidebar-inline-padding)) 0;-webkit-app-region:drag;flex:none;justify-content:flex-end;align-items:center;padding:0 12px;display:flex}._49Vfmq_topStrip ._49Vfmq_iconButton{-webkit-app-region:no-drag}._49Vfmq_topStrip+._49Vfmq_logoRow{margin-top:-12px}._49Vfmq_logoRow{box-sizing:border-box;flex:none;justify-content:flex-end;align-items:center;gap:8px;height:60px;margin-bottom:8px;padding:8px 0 8px 4px;display:flex;overflow:hidden}._49Vfmq_collapsed ._49Vfmq_logoRow{justify-content:flex-start;height:36px;margin-bottom:12px;padding:0}._49Vfmq_brand{min-width:0;color:inherit;cursor:pointer;background:0 0;border:none;flex:1;align-items:center;padding:0;display:inline-flex;overflow:hidden}._49Vfmq_brandIdentity{align-items:center;gap:8px;min-width:0;height:24px;display:inline-flex}._49Vfmq_brandMark{flex:none;justify-content:center;align-items:center;display:inline-flex}._49Vfmq_brandName{letter-spacing:.04em;align-items:center;gap:6px;min-width:0;height:24px;font-size:18px;font-weight:600;line-height:24px;display:inline-flex}._49Vfmq_fallbackBrandName{letter-spacing:0;white-space:nowrap;font-size:17px}._49Vfmq_localBuildBrand{white-space:nowrap;flex-direction:column;flex:none;justify-content:center;align-items:flex-start;gap:1px;height:24px;display:inline-flex}._49Vfmq_localBuildTitle{letter-spacing:0;font-size:12px;line-height:13px}._49Vfmq_iconButton{corner-shape:round;cursor:pointer;width:28px;height:28px;color:var(--dsw-alias-label-secondary);background:0 0;border:none;border-radius:50%;flex:none;justify-content:center;align-items:center;padding:0;display:inline-flex;position:relative}._49Vfmq_iconButton:hover{background:var(--dsw-alias-interactive-bg-hover)}._49Vfmq_collapsed ._49Vfmq_iconButton{border-radius:12px;width:36px;height:36px}._49Vfmq_collapsed ._49Vfmq_toggle ._49Vfmq_panelIcon{display:none}._49Vfmq_collapsed ._49Vfmq_toggle:hover ._49Vfmq_panelIcon{display:inline}._49Vfmq_collapsed ._49Vfmq_toggle:hover ._49Vfmq_railMark{display:none}._49Vfmq_railMark{justify-content:center;align-items:center;display:inline-flex}._49Vfmq_collapsed ._49Vfmq_iconButton{color:var(--dsw-alias-label-primary)}._49Vfmq_buildVersion{height:10px;color:var(--dsw-alias-label-primary-inverted);background:var(--dsw-alias-label-primary);font-family:var(--ds-font-family-code);white-space:nowrap;border-radius:2px;flex:none;align-items:center;padding:0 3px;font-size:6px;font-weight:500;line-height:10px;display:inline-flex}._49Vfmq_newSession{box-sizing:border-box;border:.5px solid var(--dsw-alias-border-l3);background:var(--dsw-alias-button-elevated-fill);height:38px;color:var(--dsw-alias-label-primary);cursor:pointer;border-radius:12px;flex:none;justify-content:center;align-items:center;gap:6px;margin:0 2px 12px;padding:8px 16px;font-size:14px;font-weight:500;line-height:22px;display:flex;overflow:hidden}._49Vfmq_newSession:hover{background:var(--dsw-alias-button-floating-hover)}[data-platform=darwin] ._49Vfmq_newSession{background:color-mix(in srgb, var(--dsw-alias-button-elevated-fill) 75%, transparent)}[data-platform=darwin] ._49Vfmq_newSession:hover{background:color-mix(in srgb, var(--dsw-alias-button-floating-hover) 75%, transparent)}._49Vfmq_collapsed ._49Vfmq_newSession{background:0 0;border-color:#0000;align-self:flex-start;gap:0;width:36px;height:36px;margin:0 0 12px;padding:0}._49Vfmq_collapsed ._49Vfmq_newSession:hover{background:var(--dsw-alias-interactive-bg-hover)}._49Vfmq_newSessionLabel{white-space:nowrap;max-width:200px;overflow:hidden}._49Vfmq_collapsed ._49Vfmq_newSessionLabel{max-width:0}._49Vfmq_panelList{flex-direction:column;flex:none;gap:4px;margin-bottom:8px;display:flex}._49Vfmq_panelRow{box-sizing:border-box;min-height:36px;color:var(--dsw-alias-label-primary);font:inherit;text-align:left;cursor:pointer;background:0 0;border:none;border-radius:12px;align-items:center;gap:8px;margin:0 2px;padding:7px 8px;line-height:22px;display:flex}._49Vfmq_panelRow:hover{background:var(--dsw-alias-interactive-bg-hover)}._49Vfmq_panelRow._49Vfmq_panelActive{background:var(--dsw-alias-interactive-bg-hover);color:var(--dsw-alias-label-primary)}._49Vfmq_panelRow:focus-visible{outline:2px solid var(--dsw-alias-label-primary);outline-offset:-2px}._49Vfmq_panelGlyph{flex:none;justify-content:center;align-items:center;display:inline-flex}._49Vfmq_panelTitle{text-overflow:ellipsis;white-space:nowrap;min-width:0;overflow:hidden}._49Vfmq_collapsed ._49Vfmq_panelList{gap:12px;margin-bottom:12px}._49Vfmq_collapsed ._49Vfmq_panelRow{width:36px;height:36px;color:var(--dsw-alias-label-primary);justify-content:center;margin:0;padding:0}._49Vfmq_regionArea{min-height:0;margin-left:-4px;margin-right:calc(-1 * var(--dsh-sidebar-inline-padding));flex-direction:column;flex:1;padding-left:4px;display:flex;overflow:hidden}._49Vfmq_collapsed ._49Vfmq_regionArea{margin-left:0;margin-right:0;padding-left:0}._49Vfmq_footArea{flex-direction:column;flex:none;display:flex}._49Vfmq_settingsArea,._49Vfmq_footerActions{flex:none;width:100%;min-width:0}._49Vfmq_footerActions{display:flex}._49Vfmq_collapsed ._49Vfmq_footArea{align-items:center}._49Vfmq_collapsed ._49Vfmq_settingsArea,._49Vfmq_collapsed ._49Vfmq_footerActions{justify-content:center;width:auto;display:flex}@media (prefers-reduced-motion:reduce){._49Vfmq_wide,._49Vfmq_fading>*,._49Vfmq_railIn ._49Vfmq_iconButton,._49Vfmq_railIn ._49Vfmq_newSession,._49Vfmq_railIn ._49Vfmq_panelList,._49Vfmq_railIn ._49Vfmq_footArea,._49Vfmq_railIn ._49Vfmq_regionArea{transition:none;animation:none}}";
		const tagId = "@deepseek-ai/dsh-client-ui-sidebar/SidebarRoot.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-sidebar";
			tag.dataset.pluginCss = tagId;
			tag.textContent = css;
			document.head.appendChild(tag);
		}
		var SidebarRoot_module_css_default = {
			"brand": "_49Vfmq_brand",
			"brandIdentity": "_49Vfmq_brandIdentity",
			"brandMark": "_49Vfmq_brandMark",
			"brandName": "_49Vfmq_brandName",
			"buildVersion": "_49Vfmq_buildVersion",
			"collapsed": "_49Vfmq_collapsed",
			"fading": "_49Vfmq_fading",
			"fallbackBrandName": "_49Vfmq_fallbackBrandName",
			"footArea": "_49Vfmq_footArea",
			"footerActions": "_49Vfmq_footerActions",
			"iconButton": "_49Vfmq_iconButton",
			"localBuildBrand": "_49Vfmq_localBuildBrand",
			"localBuildTitle": "_49Vfmq_localBuildTitle",
			"logoRow": "_49Vfmq_logoRow",
			"newSession": "_49Vfmq_newSession",
			"newSessionLabel": "_49Vfmq_newSessionLabel",
			"panelActive": "_49Vfmq_panelActive",
			"panelGlyph": "_49Vfmq_panelGlyph",
			"panelIcon": "_49Vfmq_panelIcon",
			"panelList": "_49Vfmq_panelList",
			"panelRow": "_49Vfmq_panelRow",
			"panelTitle": "_49Vfmq_panelTitle",
			"quietBars": "_49Vfmq_quietBars",
			"rail-fade-in": "_49Vfmq_rail-fade-in",
			"rail-in": "_49Vfmq_rail-in",
			"railIn": "_49Vfmq_railIn",
			"railMark": "_49Vfmq_railMark",
			"regionArea": "_49Vfmq_regionArea",
			"root": "_49Vfmq_root",
			"settingsArea": "_49Vfmq_settingsArea",
			"toggle": "_49Vfmq_toggle",
			"topStrip": "_49Vfmq_topStrip",
			"wide": "_49Vfmq_wide",
			"wide-in": "_49Vfmq_wide-in"
		};
		//#endregion
		//#region lib/types/client/SidebarRoot.js
		/**
		* Sidebar shell: column geometry and global panel navigation.
		* Collapse is a slide plus crossfade:
		* content freezes at its expanded width (inline style) and fades out in place
		* while the sliding column (AppFrame grid tracks) clips it — nothing reflows
		* mid-slide. At settle the wide-only content unmounts and the upper
		* controls enter the 56px rail from the same horizontal offset (one icon each,
		* same top-down order) on one fade that ends with the slide. The bottom-pinned
		* settings control only fades. The workspace/session browsing region between
		* global panel rows and the foot is the `sidebar.workspaces` registrant's,
		* and the foot holds `sidebar.settings` plus `sidebar.footer.action`; the shell
		* hands them the wide flag (plus an expand request callback for the browser).
		*
		* The column also owns whether the scroll regions nested in it draw a
		* scrollbar at all: the shell tracks the pointer and rebinds ui-theme's
		* scrollbar indirection away while it is elsewhere, so a list the user is not
		* pointing at carries no bar.
		*/
		/** ZenForge brand mark on a 24-unit grid (replaces the upstream whale). */
		function ZenForgeMark({ size = 24 }) {
			return (0, react_jsx_runtime.jsx)("svg", {
				width: size,
				height: size,
				viewBox: "0 0 24 24",
				fill: "none",
				"aria-hidden": "true",
				children: (0, react_jsx_runtime.jsx)("path", {
					d: "M4 4h16v3.2L9.4 17H20v3H4v-3.2L14.6 7H4z",
					fill: "currentColor"
				})
			});
		}
		/** Wide-content unmount delay; matches the 150ms wide-content fade-out. */
		const COLLAPSE_SETTLE_MS = 150;
		/**
		* How long the column's scrollbars stay drawn after the pointer leaves it.
		* The bar is a pointer affordance here, and hiding it on the leave event
		* itself makes it blink out while the pointer is only crossing the column's
		* edge — on the way to the conversation, or around a portalled menu.
		*/
		const SCROLLBAR_LINGER_MS = 2e3;
		/** Format complete-build metadata for the local brand badge. */
		function localBuildVersion() {
			return `0.1.6-alpha.2-ddefc45-dirty`;
		}
		/** Each panel row subscribes only to its own selection state. */
		function PanelRow({ id, label, wide, usePanelInfo, selectPanel, renderSlot }) {
			const active = usePanelInfo((info) => info.activePanelId === id);
			return (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tooltip, {
				label,
				delayMs: 500,
				disabled: wide,
				children: (0, react_jsx_runtime.jsxs)("button", {
					type: "button",
					className: clsx(SidebarRoot_module_css_default.panelRow, active && SidebarRoot_module_css_default.panelActive),
					"aria-label": label,
					"aria-current": active ? "page" : void 0,
					onClick: () => {
						selectPanel(id);
					},
					children: [(0, react_jsx_runtime.jsx)("span", {
						className: SidebarRoot_module_css_default.panelGlyph,
						"aria-hidden": "true",
						children: renderSlot("sidebar.panellist", {
							size: wide ? 16 : 18,
							active
						}, { only: id })
					}), wide && (0, react_jsx_runtime.jsx)("span", {
						className: clsx(SidebarRoot_module_css_default.panelTitle, SidebarRoot_module_css_default.wide),
						children: label
					})]
				})
			});
		}
		/**
		* Render the sidebar column shell.
		* @param props - composed slot props (runtime share + injected callbacks, contract/slots.ts).
		* @returns the sidebar element tree.
		*/
		function SidebarRoot({ collapsed, width, startSession, toggleSidebar, selectPanel, usePanels, usePanelInfo, t, renderSlot }) {
			const panels = usePanels((snapshot) => snapshot);
			const [settled, setSettled] = (0, react.useState)(collapsed);
			(0, react.useEffect)(() => {
				if (!collapsed) {
					setSettled(false);
					return;
				}
				const timer = window.setTimeout(() => {
					setSettled(true);
				}, COLLAPSE_SETTLE_MS);
				return () => {
					window.clearTimeout(timer);
				};
			}, [collapsed]);
			const windowsTitlebar = document.documentElement.hasAttribute("data-windows-titlebar");
			const wide = windowsTitlebar ? !collapsed : !collapsed || !settled;
			const lastWideWidth = (0, react.useRef)(width);
			if (!collapsed) lastWideWidth.current = width;
			const everWide = (0, react.useRef)(!collapsed);
			if (!collapsed) everWide.current = true;
			const column = (0, react.useRef)(null);
			const [pointerInside, setPointerInside] = (0, react.useState)(false);
			const lingerTimer = (0, react.useRef)(void 0);
			const armLinger = () => {
				if (lingerTimer.current !== void 0) return;
				lingerTimer.current = window.setTimeout(() => {
					lingerTimer.current = void 0;
					setPointerInside(false);
				}, SCROLLBAR_LINGER_MS);
			};
			const cancelLinger = () => {
				window.clearTimeout(lingerTimer.current);
				lingerTimer.current = void 0;
			};
			(0, react.useEffect)(() => {
				if (!pointerInside) return;
				const onMove = (event) => {
					const rect = column.current?.getBoundingClientRect();
					/* v8 ignore next -- the listener only exists while the column is mounted and revealed. */
					if (rect === void 0) return;
					if (event.clientX >= rect.left && event.clientX < rect.right && event.clientY >= rect.top && event.clientY < rect.bottom) cancelLinger();
					else armLinger();
				};
				document.addEventListener("pointermove", onMove);
				return () => {
					document.removeEventListener("pointermove", onMove);
					cancelLinger();
				};
			}, [pointerInside]);
			const buildVersion = localBuildVersion();
			const darwinDesktop = (0, _deepseek_ai_dsh_client_ui_primitives.isDarwinDesktop)();
			const toggle = (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tooltip, {
				label: collapsed ? t("toggle.open") : t("toggle.collapse"),
				delayMs: 500,
				children: (0, react_jsx_runtime.jsxs)("button", {
					type: "button",
					className: clsx(SidebarRoot_module_css_default.iconButton, SidebarRoot_module_css_default.toggle),
					"aria-label": collapsed ? t("toggle.open") : t("toggle.collapse"),
					onClick: () => {
						toggleSidebar();
					},
					children: [
						!wide && !windowsTitlebar && (0, react_jsx_runtime.jsx)("span", {
							className: SidebarRoot_module_css_default.railMark,
							"aria-hidden": "true",
							children: renderSlot("sidebar.brand.mark", { size: 24 }, { fallback: (0, react_jsx_runtime.jsx)(ZenForgeMark, { size: 24 }) })
						}),
						(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconPanelLeftOutline16, {
							className: SidebarRoot_module_css_default.panelIcon,
							size: wide || windowsTitlebar ? 16 : 18
						}),
						!wide && renderSlot("sidebar.toggle.badge", {})
					]
				})
			});
			return (0, react_jsx_runtime.jsxs)("div", {
				ref: column,
				className: clsx(SidebarRoot_module_css_default.root, !wide && SidebarRoot_module_css_default.collapsed, !wide && everWide.current && SidebarRoot_module_css_default.railIn, collapsed && wide && SidebarRoot_module_css_default.fading, !pointerInside && SidebarRoot_module_css_default.quietBars),
				style: wide ? { width: collapsed ? lastWideWidth.current : width } : void 0,
				onPointerEnter: () => {
					cancelLinger();
					setPointerInside(true);
				},
				onPointerLeave: () => {
					armLinger();
				},
				children: [
					darwinDesktop && (0, react_jsx_runtime.jsx)("div", {
						className: SidebarRoot_module_css_default.topStrip,
						children: toggle
					}),
					(0, react_jsx_runtime.jsxs)("div", {
						className: SidebarRoot_module_css_default.logoRow,
						children: [wide && (0, react_jsx_runtime.jsx)("button", {
							type: "button",
							className: clsx(SidebarRoot_module_css_default.brand, SidebarRoot_module_css_default.wide),
							"aria-label": t("session.new.label"),
							onClick: () => {
								startSession();
							},
							children: (0, react_jsx_runtime.jsxs)("span", {
								className: SidebarRoot_module_css_default.brandIdentity,
								"aria-hidden": "true",
								children: [(0, react_jsx_runtime.jsx)("span", {
									className: SidebarRoot_module_css_default.brandMark,
									children: renderSlot("sidebar.brand.mark", { size: 24 }, { fallback: (0, react_jsx_runtime.jsx)(ZenForgeMark, { size: 24 }) })
								}), (0, react_jsx_runtime.jsx)("span", {
									className: SidebarRoot_module_css_default.brandName,
									children: renderSlot("sidebar.brand.name", {}, { fallback: buildVersion === void 0 ? (0, react_jsx_runtime.jsx)("span", {
										className: SidebarRoot_module_css_default.fallbackBrandName,
										children: t("brand.localBuild")
									}) : (0, react_jsx_runtime.jsxs)("span", {
										className: SidebarRoot_module_css_default.localBuildBrand,
										children: [(0, react_jsx_runtime.jsx)("span", {
											className: SidebarRoot_module_css_default.localBuildTitle,
											children: t("brand.localBuild")
										}), (0, react_jsx_runtime.jsx)("span", {
											className: SidebarRoot_module_css_default.buildVersion,
											children: buildVersion
										})]
									}) })
								})]
							})
						}), !darwinDesktop && toggle]
					}),
					(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tooltip, {
						label: t("session.new.label"),
						delayMs: 500,
						disabled: wide,
						children: (0, react_jsx_runtime.jsxs)("button", {
							type: "button",
							className: SidebarRoot_module_css_default.newSession,
							"aria-label": t("session.new.label"),
							onClick: () => {
								startSession();
							},
							children: [(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconNewChatOutline16, { size: wide ? 14 : windowsTitlebar ? 16 : 18 }), wide && (0, react_jsx_runtime.jsx)("span", {
								className: clsx(SidebarRoot_module_css_default.newSessionLabel, SidebarRoot_module_css_default.wide),
								children: t("session.new")
							})]
						})
					}),
					panels.length > 0 && (0, react_jsx_runtime.jsx)("nav", {
						className: SidebarRoot_module_css_default.panelList,
						"aria-label": t("panels.label"),
						children: panels.map(({ id, label }) => (0, react_jsx_runtime.jsx)(PanelRow, {
							id,
							label,
							wide,
							usePanelInfo,
							selectPanel,
							renderSlot
						}, id))
					}),
					(0, react_jsx_runtime.jsx)("div", {
						className: SidebarRoot_module_css_default.regionArea,
						children: renderSlot("sidebar.workspaces", {
							wide,
							expandSidebar: () => {
								if (collapsed) toggleSidebar();
							}
						})
					}),
					(0, react_jsx_runtime.jsxs)("div", {
						className: SidebarRoot_module_css_default.footArea,
						children: [(0, react_jsx_runtime.jsx)("div", {
							className: SidebarRoot_module_css_default.footerActions,
							children: renderSlot("sidebar.footer.action", { wide })
						}), (0, react_jsx_runtime.jsx)("div", {
							className: SidebarRoot_module_css_default.settingsArea,
							children: renderSlot("sidebar.settings", { wide })
						})]
					})
				]
			});
		}
		//#endregion
		//#region lib/types/client/locales.js
		/** `sidebar` namespace dictionaries for shell controls and global panels. */
		/** Simplified Chinese dictionary (the key-set source of truth). */
		const zh = {
			"session.new": "新会话",
			"session.new.label": "新建会话",
			"toggle.open": "打开侧边栏",
			"toggle.collapse": "收起侧边栏",
			"panels.label": "全局面板"
		};
		/** English dictionary, checked complete against the zh key set. */
		const en = {
			"session.new": "New Session",
			"session.new.label": "New session",
			"toggle.open": "Open sidebar",
			"toggle.collapse": "Collapse sidebar",
			"panels.label": "Global panels"
		};
		//#endregion
		//#region lib/types/client/index.js
		/** Dictionary namespace owned by this plugin. */
		const NS = "sidebar";
		/** Services required by the sidebar plugin. */
		const inject = [
			"slots",
			"layout",
			"uiWorkspace",
			"locale"
		];
		/** Registers the sidebar shell and its service callbacks.
		* @param ctx - Client root context.
		*/
		function apply(ctx) {
			const workspaceNavigation = ctx.get("uiWorkspace");
			ctx.effect(() => ctx.locale.register(NS, {
				zh,
				en
			}), "ui-sidebar: dictionaries");
			const panels = (0, _deepseek_ai_dsh_client_store.createSnapshotStore)([]);
			const syncPanels = () => {
				const next = ctx.slots.entriesOfSlot("sidebar.panellist").map(({ options }) => {
					const id = options.id;
					return {
						id,
						order: options.order ?? 0,
						label: (0, _deepseek_ai_dsh_client_ui_slots.resolveSlotLabel)(options.label) ?? id
					};
				}).sort((a, b) => a.order - b.order);
				const previous = panels.getSnapshot();
				if (previous.length === next.length && previous.every((panel, index) => {
					const candidate = next[index];
					return panel.id === candidate.id && panel.order === candidate.order && panel.label === candidate.label;
				})) return;
				panels.set(next);
			};
			ctx.effect(() => ctx.slots.subscribe("sidebar.panellist", syncPanels), "ui-sidebar: panel entries");
			ctx.effect(() => ctx.locale.subscribe(syncPanels), "ui-sidebar: panel labels");
			const injectProps = () => ({
				startSession: (workspaceId) => {
					workspaceNavigation.startSession(workspaceId);
				},
				toggleSidebar: () => {
					ctx.layout.toggleSidebar();
				},
				selectPanel: (id) => {
					ctx.layout.selectPanel(id);
				},
				hooks: { panels }
			});
			ctx.slots.inject("sidebar", () => ctx.slots.register({
				name: "sidebar",
				locale: NS,
				children: {
					"sidebar.brand.mark": {
						kind: "single",
						scope: "root"
					},
					"sidebar.brand.name": {
						kind: "single",
						scope: "root"
					},
					"sidebar.toggle.badge": {
						kind: "single",
						scope: "root"
					},
					"sidebar.panellist": {
						kind: "list",
						scope: "root"
					},
					"sidebar.workspaces": {
						kind: "single",
						scope: "root"
					},
					"sidebar.settings": {
						kind: "single",
						scope: "root"
					},
					"sidebar.footer.action": {
						kind: "list",
						scope: "root"
					}
				},
				inject: injectProps
			}, SidebarRoot));
			ctx.slots.inject("conversation.session.header.leading", () => ctx.slots.register({
				name: "conversation.session.header.leading",
				locale: NS,
				inject: injectProps
			}, HeaderLeadingControls));
			syncPanels();
		}
		//#endregion
		exports.apply = apply;
		exports.inject = inject;
		return module.exports;
	}
});

//# sourceMappingURL=client.js.map