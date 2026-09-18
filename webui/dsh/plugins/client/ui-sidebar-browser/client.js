window.__ModuleLoader__.load({
	id: "@deepseek-ai/dsh-client-ui-sidebar-browser",
	factory: (require) => {
		var module = { exports: {} };
		var exports = module.exports;
		Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
		let react_jsx_runtime = require("react/jsx-runtime");
		let react = require("react");
		let _deepseek_ai_dsh_client_ui_primitives = require("@deepseek-ai/dsh-client-ui-primitives");
		let _deepseek_ai_dsh_client_store = require("@deepseek-ai/dsh-client-store");
		/**
		* Owns the application-known URL history and the iframe observation state machine.
		* The first load for a request keeps its URL authoritative; another load marks it unknown.
		*/
		var BrowserNavigation = class BrowserNavigation {
			value;
			/**
			* @param initial - persisted state restored for this tab, or a fresh empty state.
			*/
			constructor(initial = BrowserNavigation.empty()) {
				this.value = initial;
			}
			/**
			* Create state before a tab has a controlled navigation target.
			* @returns empty serializable state.
			*/
			static empty() {
				return {
					entries: [],
					index: -1,
					request: void 0,
					navigation: { status: "empty" },
					failure: void 0
				};
			}
			/**
			* Read the selected application-history entry.
			* @param state - serializable tab state.
			* @returns the current target, if any.
			*/
			static current(state) {
				return state === void 0 || state.index < 0 ? void 0 : state.entries[state.index];
			}
			/**
			* Test whether the Web carrier can use the preceding application-history entry.
			* @param state - serializable tab state.
			* @returns whether Back is available.
			*/
			static canGoBack(state) {
				return state.navigation.status !== "unknown" && state.index > 0;
			}
			/**
			* Test whether the Web carrier can use the following application-history entry.
			* @param state - serializable tab state.
			* @returns whether Forward is available.
			*/
			static canGoForward(state) {
				return state.navigation.status !== "unknown" && state.index >= 0 && state.index < state.entries.length - 1;
			}
			/** Current immutable serializable state. */
			get snapshot() {
				return this.value;
			}
			/** Whether the Web iframe can safely use the application-owned Back entry. */
			get canGoBack() {
				return BrowserNavigation.canGoBack(this.value);
			}
			/** Whether the Web iframe can safely use the application-owned Forward entry. */
			get canGoForward() {
				return BrowserNavigation.canGoForward(this.value);
			}
			/**
			* Add a controlled target and discard its stale forward branch.
			* @param target - validated canonical target.
			* @returns the new load request.
			*/
			navigate(target) {
				const entries = [...this.value.entries.slice(0, this.value.index + 1), target];
				if (entries.length > 100) entries.splice(0, entries.length - 100);
				return this.request(target, {
					...this.value,
					entries,
					index: entries.length - 1
				});
			}
			/**
			* Select the preceding application-known target.
			* @returns a new load request, or undefined when unavailable.
			*/
			back() {
				if (!this.canGoBack) return void 0;
				const index = this.value.index - 1;
				const target = this.value.entries[index];
				return this.request(target, {
					...this.value,
					index
				});
			}
			/**
			* Select the following application-known target.
			* @returns a new load request, or undefined when unavailable.
			*/
			forward() {
				if (!this.canGoForward) return void 0;
				const index = this.value.index + 1;
				const target = this.value.entries[index];
				return this.request(target, {
					...this.value,
					index
				});
			}
			/**
			* Start another load of the last application-known target.
			* @returns a new load request, or undefined before the first target.
			*/
			reload() {
				const target = BrowserNavigation.current(this.value);
				return target === void 0 ? void 0 : this.request(target, this.value);
			}
			/**
			* Record an invalid address without changing the active document state.
			* @param reason - parser refusal.
			*/
			addressFailed(reason) {
				this.value = {
					...this.value,
					failure: {
						kind: "address",
						reason
					}
				};
			}
			/**
			* Record a frame load for its captured revision.
			* @param revision - revision bound to the rendered frame.
			*/
			frameLoaded(revision) {
				const navigation = this.value.navigation;
				if (navigation.status === "empty" || navigation.revision !== revision) return;
				if (navigation.status === "loading") this.value = {
					...this.value,
					navigation: {
						status: "known",
						revision
					}
				};
				else if (navigation.status === "known") this.value = {
					...this.value,
					navigation: {
						status: "unknown",
						revision
					}
				};
			}
			request(target, basis) {
				const request = {
					revision: (this.value.request?.revision ?? 0) + 1,
					target
				};
				this.value = {
					...basis,
					request,
					navigation: {
						status: "loading",
						revision: request.revision
					},
					failure: void 0
				};
				return request;
			}
		};
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-sidebar-browser/src/client/view/Browser.module.css.mjs
		const css = ".LgqHKW_root{height:100%;min-height:0;color:var(--dsw-alias-label-primary);background:var(--dsw-alias-bg-base);flex-direction:column;flex:auto;display:flex}.LgqHKW_toolbar{box-sizing:border-box;border-bottom:.5px solid var(--dsw-alias-border-l3);flex:none;align-items:center;gap:4px;height:38px;padding:5px 6px;display:flex}.LgqHKW_tool{width:28px;height:28px;color:var(--dsw-alias-label-secondary);cursor:pointer;background:0 0;border:0;border-radius:6px;flex:none;justify-content:center;align-items:center;padding:0;display:inline-flex}.LgqHKW_tool:hover:not(:disabled){color:var(--dsw-alias-label-primary);background:var(--dsw-alias-interactive-bg-hover)}.LgqHKW_tool:disabled{color:var(--dsw-alias-label-quaternary);cursor:default}.LgqHKW_sandboxOff{color:var(--dsw-alias-state-error-primary);background:color-mix(in srgb, var(--dsw-alias-state-error-primary) 10%, transparent)}.LgqHKW_addressBox{flex:auto;min-width:0;position:relative}.LgqHKW_address{box-sizing:border-box;width:100%;height:28px;color:var(--dsw-alias-label-primary);font:var(--dsw-font-xxs-12);background:var(--dsw-alias-bg-layer-1);border:.5px solid var(--dsw-alias-border-l2);border-radius:6px;padding:0 34px 0 9px}.LgqHKW_addressGo{visibility:hidden;opacity:0;position:absolute;top:0;right:0}.LgqHKW_addressBox:focus-within .LgqHKW_addressGo{visibility:visible;opacity:1}.LgqHKW_addressUnknown{color:var(--dsw-alias-label-tertiary);background:var(--dsw-alias-bg-layer-2)}.LgqHKW_addressUnknown:focus{color:var(--dsw-alias-label-primary);background:var(--dsw-alias-bg-layer-1)}.LgqHKW_addressChanged{color:var(--dsw-alias-label-tertiary);font:var(--dsw-font-xxxs-11);pointer-events:none;background:var(--dsw-alias-bg-layer-2);padding-left:8px;line-height:16px;position:absolute;top:6px;right:8px}.LgqHKW_addressBox:focus-within .LgqHKW_addressChanged{visibility:hidden;opacity:0}.LgqHKW_address:focus{outline:1px solid var(--dsw-alias-brand-primary-new-colorprimary-new-color);outline-offset:-1px}.LgqHKW_frame{background:var(--dsw-alias-bg-base);border:0;flex:auto;width:100%;min-height:0;display:flex}.LgqHKW_start{min-height:0;color:var(--dsw-alias-label-tertiary);font:var(--dsw-font-xs-13);text-align:center;flex:auto;justify-content:center;align-items:center;padding:24px;display:flex}.LgqHKW_failure{color:var(--dsw-alias-state-error-primary);font:var(--dsw-font-xxxs-11);background:color-mix(in srgb, var(--dsw-alias-state-error-primary) 8%, transparent);flex:none;padding:6px 12px}.LgqHKW_sandboxWarning{color:var(--dsw-alias-state-warning-primary,var(--dsw-alias-state-business-primary));font:var(--dsw-font-xxxs-11);background:color-mix(in srgb, var(--dsw-alias-state-warning-primary,var(--dsw-alias-state-business-primary)) 8%, transparent);flex:none;padding:6px 12px}.LgqHKW_limit{color:var(--dsw-alias-label-tertiary);font:var(--dsw-font-xxxs-11);text-overflow:ellipsis;white-space:nowrap;border-top:.5px solid var(--dsw-alias-border-l3);flex:none;margin:0;padding:4px 10px;overflow:hidden}.LgqHKW_titleIcon{flex:none;margin-right:4px}";
		const tagId = "@deepseek-ai/dsh-client-ui-sidebar-browser/Browser.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-sidebar-browser";
			tag.dataset.pluginCss = tagId;
			tag.textContent = css;
			document.head.appendChild(tag);
		}
		var Browser_module_css_default = {
			"address": "LgqHKW_address",
			"addressBox": "LgqHKW_addressBox",
			"addressChanged": "LgqHKW_addressChanged",
			"addressGo": "LgqHKW_addressGo",
			"addressUnknown": "LgqHKW_addressUnknown",
			"failure": "LgqHKW_failure",
			"frame": "LgqHKW_frame",
			"limit": "LgqHKW_limit",
			"root": "LgqHKW_root",
			"sandboxOff": "LgqHKW_sandboxOff",
			"sandboxWarning": "LgqHKW_sandboxWarning",
			"start": "LgqHKW_start",
			"titleIcon": "LgqHKW_titleIcon",
			"tool": "LgqHKW_tool",
			"toolbar": "LgqHKW_toolbar"
		};
		//#endregion
		//#region lib/types/client/view/BrowserBody.js
		/** Browser toolbar and Web iframe renderer. */
		/** Fixed Web iframe sandbox; popups escape the sandbox while top navigation remains absent. */
		const WEB_BROWSER_SANDBOX = "allow-scripts allow-forms allow-same-origin allow-popups allow-popups-to-escape-sandbox";
		const INITIAL_BROWSER_FRAME = {
			document: void 0,
			sandboxed: true,
			loadFailed: false
		};
		function SandboxPolicyIcon({ sandboxed }) {
			return (0, react_jsx_runtime.jsxs)("svg", {
				width: "15",
				height: "15",
				viewBox: "0 0 16 16",
				fill: "none",
				"aria-hidden": true,
				children: [(0, react_jsx_runtime.jsx)("path", {
					d: _deepseek_ai_dsh_client_ui_primitives.SHIELD_OUTLINE_PATH,
					stroke: "currentColor",
					strokeWidth: _deepseek_ai_dsh_client_ui_primitives.SHIELD_OUTLINE_STROKE,
					strokeLinejoin: "round"
				}), sandboxed ? (0, react_jsx_runtime.jsx)("path", {
					d: "M12.1654 5.7552L8.9447 9.41475C8.73044 9.65816 8.53628 9.8804 8.35774 10.0423C8.1713 10.2114 7.94235 10.3717 7.64016 10.4254C7.48207 10.4535 7.32 10.4552 7.16151 10.4294C6.85843 10.3801 6.62728 10.2223 6.43836 10.0559C6.25752 9.89653 6.06037 9.67732 5.84264 9.43705L4.72925 8.20897L5.63557 7.38707L6.74897 8.61594C6.98603 8.87755 7.12974 9.03533 7.24673 9.13839C7.31033 9.19443 7.34485 9.21476 7.35823 9.22122C7.38068 9.22484 7.40352 9.22515 7.42593 9.22122C7.40522 9.22502 7.42893 9.23294 7.53583 9.136C7.65132 9.03126 7.79316 8.87139 8.02643 8.60638L11.2479 4.94763L12.1654 5.7552Z",
					fill: "currentColor"
				}) : (0, react_jsx_runtime.jsx)("path", {
					d: "M10.6074 4.40278L8.00975 6.99973L10.6074 9.59739L9.59736 10.6074L6.9997 8.00978L4.40274 10.6074L3.3927 9.59739L5.98966 6.99973L3.3927 4.40278L4.40274 3.39273L6.9997 5.98969L9.59736 3.39273L10.6074 4.40278Z",
					fill: "currentColor",
					transform: "translate(1.2 0.8)"
				})]
			});
		}
		/** Translate one parser refusal without matching display strings in logic. */
		function failureText(reason, t) {
			return t(`error.${reason}`);
		}
		function useBrowserDraft(controlledUrl, requestId) {
			const [edit, setEdit] = (0, react.useState)();
			return [edit !== void 0 && edit.requestId === requestId ? edit.value : controlledUrl ?? "", (draft) => {
				setEdit({
					requestId,
					value: draft
				});
			}];
		}
		/** Browser tab renderer for a controller-owned URL state and Web iframe carrier. */
		function BrowserBody(props) {
			const { goBack, goForward, loadUrl, mount, reload, reportLoaded, reportLoadFailed, toggleSandbox, useBrowserFrame, useStore, useTabInfo, t } = props;
			const { tab } = useTabInfo();
			const state = useStore((snapshot) => snapshot.byTab[tab.id]) ?? BrowserNavigation.empty();
			const initialState = (0, react.useRef)(state);
			const initialUrl = (0, react.useRef)(tab.navigation.params?.url);
			const current = BrowserNavigation.current(state);
			const [draft, setDraft] = useBrowserDraft(current?.url ?? initialUrl.current, state.request?.revision);
			const [mountCount, setMountCount] = (0, react.useState)(0);
			(0, react.useEffect)(() => {
				mount(tab.id, tab.signal, window.location.origin, initialState.current);
				setMountCount((count) => count + 1);
			}, [
				mount,
				tab.id,
				tab.signal
			]);
			(0, react.useEffect)(() => {
				if (mountCount === 0) return;
				if (BrowserNavigation.current(initialState.current) !== void 0) {
					reload(tab.id);
					return;
				}
				const url = initialUrl.current;
				if (url !== void 0) loadUrl(tab.id, url);
			}, [
				loadUrl,
				mountCount,
				reload,
				tab.id
			]);
			const { document, sandboxed, loadFailed } = useBrowserFrame(tab.id) ?? INITIAL_BROWSER_FRAME;
			const navigationUnknown = state.navigation.status === "unknown";
			const externalUrl = navigationUnknown ? void 0 : current?.url;
			const submit = (event) => {
				event.preventDefault();
				loadUrl(tab.id, draft);
			};
			const failure = state.failure === void 0 ? void 0 : failureText(state.failure.reason, t);
			const placeholder = current === void 0 ? t("start") : t("loading");
			return (0, react_jsx_runtime.jsxs)("div", {
				className: Browser_module_css_default.root,
				children: [
					(0, react_jsx_runtime.jsxs)("form", {
						className: Browser_module_css_default.toolbar,
						onSubmit: submit,
						children: [
							(0, react_jsx_runtime.jsx)("button", {
								type: "button",
								className: Browser_module_css_default.tool,
								"aria-label": t("back"),
								title: t("back"),
								disabled: !BrowserNavigation.canGoBack(state),
								onClick: () => {
									goBack(tab.id);
								},
								children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronLeftOutline14, {})
							}),
							(0, react_jsx_runtime.jsx)("button", {
								type: "button",
								className: Browser_module_css_default.tool,
								"aria-label": t("forward"),
								title: t("forward"),
								disabled: !BrowserNavigation.canGoForward(state),
								onClick: () => {
									goForward(tab.id);
								},
								children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronRightOutline14, {})
							}),
							(0, react_jsx_runtime.jsx)("button", {
								type: "button",
								className: Browser_module_css_default.tool,
								"aria-label": t("reload"),
								title: t("reload"),
								disabled: current === void 0,
								onClick: () => {
									reload(tab.id);
								},
								children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconRefreshOutline14, {})
							}),
							(0, react_jsx_runtime.jsxs)("div", {
								className: Browser_module_css_default.addressBox,
								children: [
									(0, react_jsx_runtime.jsx)("input", {
										className: `${Browser_module_css_default.address} ${navigationUnknown ? Browser_module_css_default.addressUnknown : ""}`,
										value: draft,
										"aria-label": t("address.placeholder"),
										placeholder: t("address.placeholder"),
										spellCheck: false,
										onChange: (event) => {
											setDraft(event.currentTarget.value);
										}
									}),
									navigationUnknown && (0, react_jsx_runtime.jsx)("span", {
										className: Browser_module_css_default.addressChanged,
										children: t("address.changed")
									}),
									(0, react_jsx_runtime.jsx)("button", {
										type: "submit",
										className: `${Browser_module_css_default.tool} ${Browser_module_css_default.addressGo}`,
										"aria-label": t("go"),
										title: t("go"),
										children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconLinkOutline14, {})
									})
								]
							}),
							(0, react_jsx_runtime.jsx)("button", {
								type: "button",
								className: Browser_module_css_default.tool,
								"aria-label": t("external"),
								title: t("external"),
								disabled: externalUrl === void 0,
								onClick: () => {
									/* v8 ignore next -- React does not dispatch clicks from this disabled button. */
									if (externalUrl !== void 0) window.open(externalUrl, "_blank", "noopener,noreferrer");
								},
								children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconRightUpOutline16, { size: 14 })
							}),
							(0, react_jsx_runtime.jsx)("button", {
								type: "button",
								className: `${Browser_module_css_default.tool} ${sandboxed ? "" : Browser_module_css_default.sandboxOff}`,
								"aria-label": t(sandboxed ? "sandbox.disable" : "sandbox.enable"),
								title: t(sandboxed ? "sandbox.disable" : "sandbox.enable"),
								"aria-pressed": !sandboxed,
								disabled: mountCount === 0,
								onClick: () => {
									toggleSandbox(tab.id);
								},
								children: (0, react_jsx_runtime.jsx)(SandboxPolicyIcon, { sandboxed })
							})
						]
					}),
					!sandboxed && (0, react_jsx_runtime.jsx)("div", {
						className: Browser_module_css_default.sandboxWarning,
						role: "status",
						children: t("sandbox.warning")
					}),
					loadFailed && (0, react_jsx_runtime.jsx)("div", {
						className: Browser_module_css_default.failure,
						role: "status",
						children: t("web.loadFailed")
					}),
					failure !== void 0 && (0, react_jsx_runtime.jsx)("div", {
						className: Browser_module_css_default.failure,
						role: "alert",
						children: failure
					}),
					document === void 0 ? (0, react_jsx_runtime.jsx)("div", {
						className: Browser_module_css_default.start,
						children: placeholder
					}) : (0, react_jsx_runtime.jsx)("iframe", {
						className: Browser_module_css_default.frame,
						src: document.src,
						sandbox: sandboxed ? WEB_BROWSER_SANDBOX : void 0,
						referrerPolicy: "no-referrer",
						title: document.target.title,
						onLoad: () => {
							reportLoaded(tab.id, document.revision);
						},
						/* v8 ignore next -- jsdom does not dispatch React iframe error events; BrowserFrame owns the tested behavior. */
						onError: () => {
							reportLoadFailed(tab.id, document.revision);
						},
						"data-sidebar-browser-frame": true
					}, `${document.target.url}:${String(document.revision)}`),
					navigationUnknown && (0, react_jsx_runtime.jsx)("p", {
						className: Browser_module_css_default.limit,
						children: t("web.unknown")
					})
				]
			});
		}
		//#endregion
		//#region lib/types/client/view/BrowserTitle.js
		/** Browser icon and current host name. */
		function BrowserTitle({ useTabInfo, useStore }) {
			const { tab } = useTabInfo();
			const entry = useStore((state) => BrowserNavigation.current(state.byTab[tab.id]));
			return (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconGlobeOutline14, { className: Browser_module_css_default.titleIcon }), entry?.title ?? tab.title] });
		}
		//#endregion
		//#region lib/types/client/browser/BrowserFrame.js
		/** BrowserFrame interface and the current iframe implementation. */
		/** Owns transient iframe and sandbox-toggle state independently from URL navigation. */
		var IframeImpl = class {
			sandboxChanged;
			documentLoaded;
			store = (0, _deepseek_ai_dsh_client_store.createSnapshotStore)({
				document: void 0,
				sandboxed: true,
				loadFailed: false
			});
			/**
			* @param sandboxChanged - applies the new policy to the current controlled target.
			* @param documentLoaded - reports an iframe load to the navigation state machine.
			*/
			constructor(sandboxChanged, documentLoaded) {
				this.sandboxChanged = sandboxChanged;
				this.documentLoaded = documentLoaded;
			}
			/** @returns the immutable renderer snapshot. */
			getSnapshot = () => this.store.getSnapshot();
			/**
			* Subscribe to iframe or sandbox-mode changes.
			* @param listener - invalidation callback.
			* @returns the unsubscribe function.
			*/
			subscribe = (listener) => this.store.subscribe(listener);
			/** Toggle sandbox enforcement for this tab occurrence. */
			toggleSandbox() {
				const current = this.store.getSnapshot();
				const sandboxed = !current.sandboxed;
				this.store.set({
					...current,
					sandboxed
				});
				this.sandboxChanged(sandboxed);
			}
			/**
			* Publish a prepared frame from the owning controller.
			* @param document - current prepared document.
			*/
			setDocument(document) {
				this.store.set({
					...this.store.getSnapshot(),
					document,
					loadFailed: false
				});
			}
			/** Remove the current prepared frame and transient load failure. */
			clearDocument() {
				const current = this.store.getSnapshot();
				const { document } = current;
				if (document !== void 0) this.store.set({
					...current,
					document: void 0,
					loadFailed: false
				});
			}
			/** @param revision - rendered document revision reported by the iframe. */
			reportLoaded(revision) {
				this.documentLoaded(revision);
			}
			/** @param revision - rendered document revision whose iframe emitted `error`. */
			reportLoadFailed(revision) {
				const current = this.store.getSnapshot();
				if (current.document?.revision !== revision || current.loadFailed) return;
				this.store.set({
					...current,
					loadFailed: true
				});
			}
		};
		/**
		* Parse one address-bar value into the fixed protocol allowlist.
		* @param input - user or typed-open input.
		* @param applicationOrigin - current DSH document origin, blocked for HTTPS.
		* @returns a canonical target or the refusal reason.
		*/
		function parseBrowserAddress(input, applicationOrigin) {
			const trimmed = input.trim();
			if (trimmed === "") return {
				ok: false,
				reason: "empty"
			};
			if (trimmed.length > 16384) return {
				ok: false,
				reason: "invalid"
			};
			const candidate = /^[A-Za-z][A-Za-z\d+.-]*:(?!\d+(?:[/?#]|$))/u.test(trimmed) ? trimmed : `https://${trimmed}`;
			let url;
			try {
				url = new URL(candidate);
			} catch {
				return {
					ok: false,
					reason: "invalid"
				};
			}
			if (url.username !== "" || url.password !== "") return {
				ok: false,
				reason: "credentials"
			};
			if (url.protocol === "https:" || url.protocol === "http:") {
				if (applicationOrigin !== void 0 && applicationOrigin !== "null") try {
					if (url.origin === new URL(applicationOrigin).origin) return {
						ok: false,
						reason: "application-origin"
					};
				} catch {}
				return {
					ok: true,
					target: {
						kind: url.protocol === "https:" ? "https" : "http",
						url: url.href,
						title: url.hostname
					}
				};
			}
			return {
				ok: false,
				reason: "protocol"
			};
		}
		//#endregion
		//#region lib/types/client/browser/BrowserController.js
		/** Owns one Browser tab's URL state, loading lifecycle, and four navigation commands. */
		var BrowserController = class {
			options;
			/** Renderer-facing iframe and sandbox state. */
			frame;
			navigation;
			disposed = false;
			/**
			* @param options - tab identity, persistence writer, URL origin, and lifetime.
			*/
			constructor(options) {
				this.options = options;
				this.navigation = new BrowserNavigation(options.initial);
				this.frame = new IframeImpl(() => {
					this.reload();
				}, (revision) => {
					this.frameLoaded(revision);
				});
				options.signal.addEventListener("abort", () => {
					this.dispose();
				}, { once: true });
			}
			/**
			* Validate and load one address, replacing a same-address load with Reload.
			* @param value - address-bar or typed-tab value.
			*/
			loadUrl(value) {
				if (this.disposed) return;
				const parsed = parseBrowserAddress(value, this.options.applicationOrigin);
				if (!parsed.ok) {
					this.navigation.addressFailed(parsed.reason);
					this.publish();
					return;
				}
				if (BrowserNavigation.current(this.navigation.snapshot)?.url === parsed.target.url) {
					this.reload();
					return;
				}
				this.start(this.navigation.navigate(parsed.target));
			}
			/** Move to the preceding application-known address when Web history remains usable. */
			goBack() {
				if (this.disposed) return;
				const request = this.navigation.back();
				if (request !== void 0) this.start(request);
			}
			/** Move to the following application-known address when Web history remains usable. */
			goForward() {
				if (this.disposed) return;
				const request = this.navigation.forward();
				if (request !== void 0) this.start(request);
			}
			/** Reload the last application-known address without adding history. */
			reload() {
				if (this.disposed) return;
				const request = this.navigation.reload();
				if (request !== void 0) this.start(request);
			}
			start(request) {
				this.publish();
				this.frame.clearDocument();
				this.frame.setDocument({
					target: request.target,
					src: request.target.url,
					revision: request.revision
				});
			}
			frameLoaded(revision) {
				if (this.disposed) return;
				const previous = this.navigation.snapshot;
				this.navigation.frameLoaded(revision);
				if (this.navigation.snapshot !== previous) this.publish();
			}
			publish() {
				this.options.actions.replace(this.options.tabId, this.navigation.snapshot);
			}
			dispose() {
				this.disposed = true;
				this.frame.clearDocument();
			}
		};
		/**
		* Bind Browser controllers to one Session and its persistence writer.
		* @param actions - Browser store mutation face.
		* @returns a per-tab controller registry exposed as plain Slot callbacks.
		*/
		function createBrowserControllers(actions) {
			const controllers = /* @__PURE__ */ new Map();
			const controller = (tabId) => controllers.get(tabId)?.controller;
			return {
				keyedHooks: { browserFrame: (key) => controller(key)?.frame },
				mount(tabId, signal, applicationOrigin, initial) {
					if (controllers.get(tabId)?.signal === signal) return;
					const created = new BrowserController({
						tabId,
						signal,
						applicationOrigin,
						actions,
						...initial === void 0 ? {} : { initial }
					});
					controllers.set(tabId, {
						signal,
						controller: created
					});
					signal.addEventListener("abort", () => {
						if (controllers.get(tabId)?.controller !== created) return;
						controllers.delete(tabId);
						actions.forget(tabId);
					}, { once: true });
				},
				loadUrl: (tabId, value) => {
					controller(tabId)?.loadUrl(value);
				},
				goBack: (tabId) => {
					controller(tabId)?.goBack();
				},
				goForward: (tabId) => {
					controller(tabId)?.goForward();
				},
				reload: (tabId) => {
					controller(tabId)?.reload();
				},
				toggleSandbox: (tabId) => {
					controller(tabId)?.frame.toggleSandbox();
				},
				reportLoaded: (tabId, revision) => {
					controller(tabId)?.frame.reportLoaded(revision);
				},
				reportLoadFailed: (tabId, revision) => {
					controller(tabId)?.frame.reportLoadFailed(revision);
				}
			};
		}
		//#endregion
		//#region lib/types/client/definition.js
		/** Browser tab kind. */
		const BROWSER_KIND = "browser";
		/** Browser implementation identity and keyed Slot dispatch key. */
		const BROWSER_ID = "@deepseek-ai/dsh-client-ui-sidebar-browser";
		/** Build the Browser type with locale-live copy. */
		function browserDefinition(t) {
			return {
				id: BROWSER_ID,
				kind: BROWSER_KIND,
				multiple: true,
				priority: "builtin",
				title: () => t("type.label"),
				guide: [{
					id: "new",
					order: 30,
					title: () => t("guide.title"),
					description: () => t("guide.description"),
					icon: _deepseek_ai_dsh_client_ui_primitives.IconGlobeOutline14
				}]
			};
		}
		//#endregion
		//#region lib/types/client/locales.js
		/** Locale-owned Browser tab copy. */
		const zh = {
			"type.label": "浏览器",
			"guide.title": "浏览器",
			"guide.description": "浏览 HTTP(S) 网页",
			"address.placeholder": "输入 HTTP(S) 地址",
			"address.changed": "URL 已变化",
			back: "后退",
			forward: "前进",
			reload: "刷新",
			go: "前往",
			external: "在系统浏览器中打开",
			"sandbox.disable": "关闭沙箱限制",
			"sandbox.enable": "恢复沙箱限制",
			"sandbox.warning": "沙箱限制已关闭；页面可以导航顶层应用，并使用下载、模态对话框与输入锁定。",
			start: "输入 HTTP(S) 地址开始浏览",
			loading: "正在打开…",
			"error.empty": "请输入地址。",
			"error.invalid": "这个地址无效或过长。",
			"error.protocol": "只支持 HTTP 和 HTTPS 地址；本地文件请使用文档预览。",
			"error.credentials": "地址不能包含用户名或密码。",
			"error.application-origin": "不能在嵌入浏览器中打开 DSH 应用自身。",
			"web.loadFailed": "页面报告加载失败或可能禁止嵌入；可尝试在系统浏览器中打开。",
			"web.unknown": "页面已在 iframe 内跳转；Web 模式无法读取当前 URL。"
		};
		/** English dictionary with the same keys. */
		const en = {
			"type.label": "Browser",
			"guide.title": "Browser",
			"guide.description": "Browse HTTP(S) pages",
			"address.placeholder": "Enter an HTTP(S) address",
			"address.changed": "URL changed",
			back: "Back",
			forward: "Forward",
			reload: "Reload",
			go: "Go",
			external: "Open in system browser",
			"sandbox.disable": "Disable sandbox restrictions",
			"sandbox.enable": "Restore sandbox restrictions",
			"sandbox.warning": "Sandbox restrictions are disabled; the page can navigate the top-level app and use downloads, modal dialogs, and input locks.",
			start: "Enter an HTTP(S) address to start browsing",
			loading: "Opening…",
			"error.empty": "Enter an address.",
			"error.invalid": "That address is invalid or too long.",
			"error.protocol": "Only HTTP and HTTPS addresses are supported; use Document Preview for local files.",
			"error.credentials": "Addresses cannot contain a username or password.",
			"error.application-origin": "The embedded browser cannot open the DSH application itself.",
			"web.loadFailed": "The page reported a load failure or may block embedding; try opening it in the system browser.",
			"web.unknown": "The page navigated inside the iframe; Web mode cannot read its current URL."
		};
		//#endregion
		//#region lib/types/client/browser/store.js
		/** Persisted Browser tab snapshots shared by the body and title slots. */
		/**
		* Declare the Session-scoped Browser persistence store.
		* @returns a fresh store handle for Slot registration.
		*/
		function createBrowserStore() {
			return (0, _deepseek_ai_dsh_client_store.defineStore)({
				init: () => ({ byTab: {} }),
				persist: "dsh.sidebar-browser.v1",
				actions: {
					replace: (draft, tabId, state) => {
						draft.byTab[tabId] = state;
					},
					forget: (draft, tabId) => {
						const byTab = {};
						for (const [id, state] of Object.entries(draft.byTab)) if (id !== tabId) byTab[id] = state;
						draft.byTab = byTab;
					}
				}
			});
		}
		//#endregion
		//#region lib/types/client/index.js
		/** Required Browser services. */
		const inject = [
			"slots",
			"locale",
			"sidebarRightTabs"
		];
		/** Register the Browser type, localized guide entry, body, and title. */
		function apply(ctx) {
			const namespace = "sidebarBrowser";
			const t = ctx.locale.bind(namespace);
			const store = createBrowserStore();
			ctx.effect(() => ctx.locale.register(namespace, {
				zh,
				en
			}), "ui-sidebar-browser.copy");
			ctx.effect(() => ctx.sidebarRightTabs.register(browserDefinition(t)), "ui-sidebar-browser.type");
			ctx.effect(() => ctx.slots.inject("sidebar.right.pane.tab", () => ctx.slots.register({
				name: "sidebar.right.pane.tab",
				key: BROWSER_ID,
				locale: namespace,
				store,
				inject: (_sessionId, actions) => createBrowserControllers(actions)
			}, BrowserBody)), "ui-sidebar-browser.body");
			ctx.effect(() => ctx.slots.inject("sidebar.right.pane.tab.title", () => ctx.slots.register({
				name: "sidebar.right.pane.tab.title",
				key: BROWSER_ID,
				store
			}, BrowserTitle)), "ui-sidebar-browser.title");
		}
		//#endregion
		exports.apply = apply;
		exports.inject = inject;
		return module.exports;
	}
});

//# sourceMappingURL=client.js.map