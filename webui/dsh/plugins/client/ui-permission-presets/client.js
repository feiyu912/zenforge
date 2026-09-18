window.__ModuleLoader__.load({
	id: "@deepseek-ai/dsh-client-ui-permission-presets",
	factory: (require) => {
		var module = { exports: {} };
		var exports = module.exports;
		Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
		let _deepseek_ai_dsh_client_store = require("@deepseek-ai/dsh-client-store");
		let react_jsx_runtime = require("react/jsx-runtime");
		let react = require("react");
		let _deepseek_ai_dsh_client_ui_primitives = require("@deepseek-ai/dsh-client-ui-primitives");
		//#region lib/types/client/catalog.js
		/** Identity-stable process permission catalog shared by both selection surfaces. */
		/** One latest-result-wins catalog reader for the whole browser process. */
		var PermissionCatalogDirectory = class {
			ctx;
			/** Complete snapshot consumed by both the slash popup and composer seat. */
			store = (0, _deepseek_ai_dsh_client_store.createSnapshotStore)({ value: null });
			/**
			* One tick per invalidation (a catalog notification or a connection-generation
			* change), published before the replacement read starts. Consumers that must
			* drop displayed options subscribe here instead of to {@link store}, whose
			* publications also settle a read a displayed surface is waiting for.
			*/
			invalidations = (0, _deepseek_ai_dsh_client_store.createSnapshotStore)({ count: 0 });
			connection;
			stopCatalog;
			stopGeneration;
			generationId;
			initialized = false;
			epoch = 0;
			pending;
			failure = /* @__PURE__ */ new Error("permission catalog has no complete value");
			disposed = false;
			/**
			* Subscribe to both invalidation sources before the first read, closing the
			* install/read race.
			* @param ctx - root Client context carrying Remote and Connection.
			*/
			constructor(ctx) {
				this.ctx = ctx;
				this.connection = ctx.get("connection");
				this.stopCatalog = ctx.remote.$on("permission-presets/catalog-changed", () => {
					this.invalidate();
					this.refresh();
				});
				this.stopGeneration = this.connection.generation.subscribe(() => {
					this.syncGeneration();
				});
				this.syncGeneration();
			}
			/**
			* Publish one invalidation tick for consumers holding displayed options.
			* Neither caller can run after disposal: `dispose()` unsubscribes the
			* catalog-changed listener, and `syncGeneration()` returns early when the
			* directory is disposed.
			*/
			invalidate() {
				this.invalidations.set({ count: this.invalidations.getSnapshot().count + 1 });
			}
			/** Force a fresh complete read for the active connection generation. */
			refresh() {
				if (this.disposed) return;
				const generationId = this.connection.generation.getSnapshot()?.id;
				if (generationId === void 0) return;
				if (generationId !== this.generationId) {
					this.syncGeneration();
					return;
				}
				this.startRead(generationId);
			}
			/**
			* Resolve a complete current-generation catalog for an imperative popup
			* open. An active refresh settles before a retained value can be reused.
			* @returns The active Host generation's complete permission catalog.
			*/
			async load() {
				if (this.pending === void 0 && this.store.getSnapshot().value === null) this.refresh();
				while (!this.disposed) {
					const generationId = this.connection.generation.getSnapshot()?.id;
					if (generationId === void 0) throw new Error("permission catalog has no active Host connection");
					if (generationId !== this.generationId) this.syncGeneration();
					const pending = this.pending;
					if (pending !== void 0) {
						await pending;
						continue;
					}
					const state = this.store.getSnapshot();
					if (state.value !== null) return state.value;
					throw this.failure;
				}
				throw new Error("permission catalog directory is disposed");
			}
			/** Stop subscriptions and revoke every late settlement's write access. */
			dispose() {
				if (this.disposed) return;
				this.disposed = true;
				++this.epoch;
				this.pending = void 0;
				this.stopGeneration();
				this.stopCatalog();
			}
			/** Observe generation loss/replacement and hard-clear the old Host value. */
			syncGeneration() {
				if (this.disposed) return;
				const generationId = this.connection.generation.getSnapshot()?.id;
				if (this.initialized && generationId === this.generationId) return;
				if (this.initialized) this.invalidate();
				this.initialized = true;
				this.generationId = generationId;
				++this.epoch;
				this.pending = void 0;
				this.failure = /* @__PURE__ */ new Error("permission catalog has no complete value");
				this.store.set({ value: null });
				if (generationId !== void 0) this.startRead(generationId);
			}
			/** Start one independent read; the newest epoch in the same generation wins. */
			startRead(generationId) {
				const epoch = ++this.epoch;
				this.failure = /* @__PURE__ */ new Error("permission catalog has no complete value");
				const operation = this.ctx.remote.permissionPresets.catalog().then((result) => {
					if (!result.ok) throw new Error(`${result.error.code}: ${result.error.message}`);
					if (!this.accepts(epoch, generationId)) return;
					this.store.set({ value: result.value });
				}).catch((error) => {
					if (!this.accepts(epoch, generationId)) return;
					this.failure = error instanceof Error ? error : new Error(String(error));
					this.store.set({ value: null });
				}).finally(() => {
					if (this.pending === operation) this.pending = void 0;
				});
				this.pending = operation;
			}
			/** Fence by disposal, refresh epoch, and the actual Connection generation. */
			accepts(epoch, generationId) {
				return !this.disposed && epoch === this.epoch && generationId === this.generationId && this.connection.generation.getSnapshot()?.id === generationId;
			}
		};
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
		//#region lib/types/client/locales.js
		/** `settings.permission` namespace dictionaries (the Permission row's copy). */
		/** Locale namespace shared by both current-session permission pickers. */
		const PERMISSION_ACCESS_NS = "permission.access";
		/** Simplified Chinese dictionary (the key-set source of truth). */
		const zh = {
			"title": "权限",
			"description": "选择新会话的默认权限模式",
			"loading": "加载中",
			"unavailable": "不可用",
			"preset.readOnly": "仅可查看",
			"preset.workspaceWrite": "工作区内修改",
			"preset.fullAccess": "完全权限",
			"confirm.title": "确认启用完全权限？",
			"confirm.description": "启用完全权限后，新会话将减少确认步骤，并且可以直接执行更多操作，包括敏感操作、文件修改或外部命令。仅建议在你信任后续任务时使用。",
			"confirm.acknowledge": "我已了解风险，并愿意继续",
			"confirm.cancel": "取消",
			"confirm.enable": "启用完全权限"
		};
		/** English dictionary, checked complete against the zh key set. */
		const en = {
			"title": "Permission",
			"description": "Choose the default permission mode for new sessions",
			"loading": "Loading",
			"unavailable": "Unavailable",
			"preset.readOnly": "Read Only",
			"preset.workspaceWrite": "Workspace Write",
			"preset.fullAccess": "Full access",
			"confirm.title": "Enable Full access?",
			"confirm.description": "Full access lets new sessions reduce confirmation steps and perform more actions directly, including sensitive operations, file changes, or external commands. Only use it when you trust subsequent tasks.",
			"confirm.acknowledge": "I understand the risks and want to continue",
			"confirm.cancel": "Cancel",
			"confirm.enable": "Enable Full access"
		};
		/** Simplified Chinese dictionary for the current-session popup gate. */
		const accessZh = {
			"mode": "访问模式，当前：{name}",
			"close": "关闭",
			"preset.readOnly": "仅可查看",
			"preset.workspaceWrite": "工作区内修改",
			"preset.fullAccess": "完全权限",
			"confirm.title": "确认启用完全权限？",
			"confirm.description": "启用完全权限后，智能体将减少确认步骤，并且可以直接执行更多操作，包括敏感操作、文件修改或外部命令。仅建议在你信任当前任务时使用。",
			"confirm.acknowledge": "我已了解风险，并愿意继续",
			"confirm.cancel": "取消",
			"confirm.enable": "启用完全权限",
			"auto.label": "Auto review",
			"auto.badge": "EXP",
			"auto.description": "无沙箱运行；每次原生工具调用和 PTC 内层调用前由同一模型进行实验性审查。",
			"auto.confirm.title": "确认启用 Auto review（实验）？",
			"auto.confirm.description": "Auto review 不使用沙箱。每次原生工具调用和 PTC 内层调用前，都会由与当前 agent 相同的模型进行审查。此功能仍属实验性，可能误放行或误拒绝，并会消耗额外 token。",
			"auto.confirm.acknowledge": "我已了解这些风险，并愿意继续",
			"auto.confirm.enable": "启用 Auto review"
		};
		/** English dictionary for the current-session popup gate. */
		const accessEn = {
			"mode": "Access mode, current: {name}",
			"close": "Close",
			"preset.readOnly": "Read Only",
			"preset.workspaceWrite": "Workspace Write",
			"preset.fullAccess": "Full access",
			"confirm.title": "Enable Full access?",
			"confirm.description": "Full access reduces confirmation steps and lets the agent perform more actions directly, including sensitive operations, file changes, or external commands. Only use it when you trust the current task.",
			"confirm.acknowledge": "I understand the risks and want to continue",
			"confirm.cancel": "Cancel",
			"confirm.enable": "Enable Full access",
			"auto.label": "Auto review",
			"auto.badge": "EXP",
			"auto.description": "Run without a sandbox after an experimental same-model review of every native tool call and PTC inner call.",
			"auto.confirm.title": "Enable Auto review (experimental)?",
			"auto.confirm.description": "Auto review runs without a sandbox. Before every native tool call and PTC inner call, the same model as the current agent reviews whether to allow it. This feature is experimental, can falsely allow or deny actions, and uses additional tokens.",
			"auto.confirm.acknowledge": "I understand these risks and want to continue",
			"auto.confirm.enable": "Enable Auto review"
		};
		//#endregion
		//#region lib/types/client/presentation.js
		/** Machine value of the preset that requires an explicit GUI risk gate. */
		const FULL_ACCESS_PRESET = "danger-full-access";
		const PRESET_LABEL_KEYS = new Map([
			["read-only", "preset.readOnly"],
			["workspace-write", "preset.workspaceWrite"],
			[FULL_ACCESS_PRESET, "preset.fullAccess"]
		]);
		const DEFAULT_PRESET_LABELS = {
			"preset.readOnly": en["preset.readOnly"],
			"preset.workspaceWrite": en["preset.workspaceWrite"],
			"preset.fullAccess": en["preset.fullAccess"]
		};
		/**
		* Convert conventional kebab-case preset names into user-facing title case.
		* @param name - host-supplied preset label or key.
		* @returns the title-cased conventional key, or a non-kebab label unchanged.
		*/
		function displayPresetName(name) {
			if (!/^[a-z0-9]+(-[a-z0-9]+)*$/.test(name)) return name;
			return name.split("-").map((word) => word.charAt(0).toUpperCase() + word.slice(1)).join(" ");
		}
		/**
		* Render a permission preset under its product label.
		* @param value - preset machine value.
		* @param name - host-supplied preset name.
		* @param t - optional locale dictionary lookup for built-in product labels.
		* @returns the built-in product label or the conventional display name.
		*/
		function displayPermissionPreset(value, name, t) {
			const key = PRESET_LABEL_KEYS.get(value);
			if (key !== void 0 && (name === value || name === DEFAULT_PRESET_LABELS[key])) return t?.(key) ?? DEFAULT_PRESET_LABELS[key];
			return displayPresetName(name);
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-permission-presets/src/client/PermissionSelect.module.css.mjs
		const css$1 = ".ERC-Nq_trigger{min-width:0;max-width:220px;height:28px;color:var(--dsw-alias-label-secondary);cursor:pointer;background:0 0;border:none;border-radius:24px;outline:none;align-items:center;gap:4px;padding:0 4px 0 8px;font-size:13px;font-weight:500;line-height:20px;display:inline-flex}.ERC-Nq_trigger:hover:not(:disabled){background:var(--dsw-alias-interactive-bg-hover)}.ERC-Nq_trigger:focus-visible{box-shadow:0 0 0 2px var(--dsw-alias-border-l3)}.ERC-Nq_trigger:disabled{color:var(--dsw-alias-label-dimmed);cursor:default}.ERC-Nq_triggerIcon{flex:none;display:inline-flex}.ERC-Nq_triggerIcon svg{width:14px;height:14px}.ERC-Nq_triggerLabel{text-overflow:ellipsis;white-space:nowrap;min-width:0;overflow:hidden}.ERC-Nq_optionLabel{align-items:baseline;gap:4px;min-width:0;max-width:100%;display:inline-flex}.ERC-Nq_optionLabelText{text-overflow:ellipsis;white-space:nowrap;min-width:0;overflow:hidden}.ERC-Nq_badge{color:var(--dsw-alias-label-tertiary);letter-spacing:.2px;flex:none;align-self:flex-start;margin-top:-1px;font-size:8px;font-weight:600;line-height:10px}.ERC-Nq_chevron{color:var(--dsw-alias-label-caption);flex:none;transition:transform .12s;display:inline-flex}@container (width<=460px){.ERC-Nq_trigger:has(.ERC-Nq_triggerIcon) .ERC-Nq_triggerLabel{display:none}}.ERC-Nq_chevronOpen{transform:rotate(180deg)}";
		const tagId$1 = "@deepseek-ai/dsh-client-ui-permission-presets/PermissionSelect.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$1) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-permission-presets";
			tag.dataset.pluginCss = tagId$1;
			tag.textContent = css$1;
			document.head.appendChild(tag);
		}
		var PermissionSelect_module_css_default = {
			"badge": "ERC-Nq_badge",
			"chevron": "ERC-Nq_chevron",
			"chevronOpen": "ERC-Nq_chevronOpen",
			"optionLabel": "ERC-Nq_optionLabel",
			"optionLabelText": "ERC-Nq_optionLabelText",
			"trigger": "ERC-Nq_trigger",
			"triggerIcon": "ERC-Nq_triggerIcon",
			"triggerLabel": "ERC-Nq_triggerLabel"
		};
		//#endregion
		//#region lib/types/client/PermissionSelect.js
		const permissionGlyphs = new Map([
			["read-only", (0, react_jsx_runtime.jsxs)("svg", {
				width: "16",
				height: "16",
				viewBox: "0 0 16 16",
				fill: "none",
				"aria-hidden": true,
				children: [(0, react_jsx_runtime.jsx)("path", {
					d: _deepseek_ai_dsh_client_ui_primitives.SHIELD_OUTLINE_PATH,
					stroke: "currentColor",
					strokeWidth: _deepseek_ai_dsh_client_ui_primitives.SHIELD_OUTLINE_STROKE,
					strokeLinejoin: "round"
				}), (0, react_jsx_runtime.jsx)("path", {
					d: "M12.1654 5.7552L8.9447 9.41475C8.73044 9.65816 8.53628 9.8804 8.35774 10.0423C8.1713 10.2114 7.94235 10.3717 7.64016 10.4254C7.48207 10.4535 7.32 10.4552 7.16151 10.4294C6.85843 10.3801 6.62728 10.2223 6.43836 10.0559C6.25752 9.89653 6.06037 9.67732 5.84264 9.43705L4.72925 8.20897L5.63557 7.38707L6.74897 8.61594C6.98603 8.87755 7.12974 9.03533 7.24673 9.13839C7.31033 9.19443 7.34485 9.21476 7.35823 9.22122C7.38068 9.22484 7.40352 9.22515 7.42593 9.22122C7.40522 9.22502 7.42893 9.23294 7.53583 9.136C7.65132 9.03126 7.79316 8.87139 8.02643 8.60638L11.2479 4.94763L12.1654 5.7552Z",
					fill: "currentColor"
				})]
			})],
			["workspace-write", (0, react_jsx_runtime.jsxs)("svg", {
				width: "16",
				height: "16",
				viewBox: "0 0 16 16",
				fill: "none",
				"aria-hidden": true,
				children: [
					(0, react_jsx_runtime.jsx)("path", {
						d: "M8.08887 0.251709C8.20479 0.23085 8.32486 0.241168 8.43652 0.282959L15.0215 2.75171C15.2787 2.84819 15.4492 3.09414 15.4492 3.3689V7.0105C15.4492 7.10986 15.4441 7.2081 15.4414 7.30542C15.0285 7.07175 14.5905 6.87695 14.1309 6.73022V3.82495L8.20508 1.60327L2.2793 3.82495V7.0105C2.27936 9.7171 3.4745 11.5379 5.02734 12.7947C5.01025 12.9942 5 13.1962 5 13.4001C5.00001 13.7617 5.02722 14.1169 5.08008 14.4636C2.91555 13.0393 0.961014 10.752 0.960938 7.0105V3.3689C0.960938 3.09417 1.13146 2.84821 1.38867 2.75171L7.97461 0.282959L8.08887 0.251709Z",
						fill: "currentColor"
					}),
					(0, react_jsx_runtime.jsx)("path", {
						d: "M11.3525 5.64688V6.85688H5V5.64688H11.3525Z",
						fill: "currentColor"
					}),
					(0, react_jsx_runtime.jsx)("path", {
						d: "M9.5824 8.29376V9.50376H5V8.29376H9.5824Z",
						fill: "currentColor"
					}),
					(0, react_jsx_runtime.jsx)("path", {
						d: "M14.6647 15.6852H10.0338C10.3878 15.3751 10.7567 15.0517 11.0772 14.7706C11.2531 14.6164 11.4144 14.4746 11.5511 14.3547H14.6647V15.6852Z",
						fill: "currentColor"
					}),
					(0, react_jsx_runtime.jsx)("path", {
						d: "M8.14852 14.1308L7.33925 15.4976C7.22458 15.6912 7.42245 15.9194 7.63037 15.8333L9.09785 15.2254L15.0399 10.0719L14.0905 8.97733L8.14852 14.1308Z",
						fill: "currentColor"
					})
				]
			})],
			[FULL_ACCESS_PRESET, (0, react_jsx_runtime.jsxs)("svg", {
				width: "16",
				height: "16",
				viewBox: "0 0 16 16",
				fill: "none",
				"aria-hidden": true,
				children: [
					(0, react_jsx_runtime.jsx)("path", {
						d: _deepseek_ai_dsh_client_ui_primitives.SHIELD_OUTLINE_PATH,
						stroke: "currentColor",
						strokeWidth: _deepseek_ai_dsh_client_ui_primitives.SHIELD_OUTLINE_STROKE,
						strokeLinejoin: "round"
					}),
					(0, react_jsx_runtime.jsx)("path", {
						d: "M9.10094 4.5V8.75939H7.59888V4.5H9.10094Z",
						fill: "currentColor"
					}),
					(0, react_jsx_runtime.jsx)("path", {
						d: "M9.10094 9.8114V11.5H7.59888V9.8114H9.10094Z",
						fill: "currentColor"
					})
				]
			})]
		]);
		/** Glyph for a permission option value; host-configured names outside the design set get none. */
		function permissionGlyph(value) {
			return permissionGlyphs.get(value);
		}
		function permissionLabel(value, name, t) {
			if (value === "auto") return t("auto.label");
			return displayPermissionPreset(value, name, (key) => t(key));
		}
		function optionBadge(value, t) {
			return value === "auto" ? t("auto.badge") : void 0;
		}
		/** Resolve locale-owned copy for the shipped Auto option; preserve host copy for other presets. */
		function optionDescription(option, t) {
			return option.value === "auto" ? t("auto.description") : option.description;
		}
		function PermissionSelect({ locked, select, usePermissionCatalog, useProjection, t }) {
			const selection = useProjection("permissions");
			const catalog = usePermissionCatalog((state) => state.value);
			const [pick, setPick] = (0, react.useState)(null);
			const [open, setOpen] = (0, react.useState)(false);
			const [confirmation, setConfirmation] = (0, react.useState)(null);
			const [acknowledged, setAcknowledged] = (0, react.useState)(false);
			(0, react.useEffect)(() => {
				if (!locked && selection !== void 0 && catalog !== null && (confirmation === null || catalog.options.some((option) => option.value === confirmation))) return;
				setOpen(false);
				setAcknowledged(false);
				setConfirmation(null);
			}, [
				catalog,
				confirmation,
				locked,
				selection
			]);
			if (selection === void 0 || catalog === null) return null;
			const currentValue = pick !== null && catalog.options.some((option) => option.value === pick) ? pick : selection.currentValue;
			const current = catalog.options.find((option) => option.value === currentValue);
			const currentLabel = current === void 0 ? permissionLabel(currentValue, currentValue, t) : permissionLabel(current.value, current.name, t);
			const busy = pick !== null || confirmation !== null;
			const items = catalog.options.map((option) => {
				const icon = permissionGlyph(option.value);
				const label = permissionLabel(option.value, option.name, t);
				const badge = optionBadge(option.value, t);
				return {
					id: option.value,
					label: badge === void 0 ? label : (0, react_jsx_runtime.jsxs)("span", {
						className: PermissionSelect_module_css_default.optionLabel,
						"aria-label": `${label} ${badge}`,
						children: [(0, react_jsx_runtime.jsx)("span", {
							className: PermissionSelect_module_css_default.optionLabelText,
							children: label
						}), (0, react_jsx_runtime.jsx)("sup", {
							className: PermissionSelect_module_css_default.badge,
							children: badge
						})]
					}),
					...icon === void 0 ? {} : { icon }
				};
			});
			const submit = (id) => {
				setPick(id);
				select(id).catch(() => false).then(() => {
					setPick(null);
				});
			};
			const choose = (id) => {
				setOpen(false);
				if (id === selection.currentValue) return;
				if (id === "danger-full-access" || id === "auto") {
					setAcknowledged(false);
					setConfirmation(id);
					return;
				}
				submit(id);
			};
			const closeConfirmation = () => {
				setAcknowledged(false);
				setConfirmation(null);
			};
			const confirmSelection = (id) => {
				closeConfirmation();
				submit(id);
			};
			const confirmationTitle = confirmation === "auto" ? t("auto.confirm.title") : t("confirm.title");
			const confirmationDescription = confirmation === "auto" ? t("auto.confirm.description") : t("confirm.description");
			const confirmationAcknowledge = confirmation === "auto" ? t("auto.confirm.acknowledge") : t("confirm.acknowledge");
			const confirmationEnable = confirmation === "auto" ? t("auto.confirm.enable") : t("confirm.enable");
			const currentBadge = optionBadge(currentValue, t);
			const currentAccessibleLabel = currentBadge === void 0 ? currentLabel : `${currentLabel} ${currentBadge}`;
			return (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Menu, {
				open,
				items,
				selectedId: currentValue,
				onSelect: choose,
				onClose: () => {
					setOpen(false);
				},
				side: "top",
				portal: true,
				anchor: (0, react_jsx_runtime.jsxs)("button", {
					type: "button",
					className: PermissionSelect_module_css_default.trigger,
					"aria-label": t("mode", { name: currentAccessibleLabel }),
					title: current === void 0 ? void 0 : optionDescription(current, t),
					disabled: locked || busy,
					onClick: () => {
						setOpen(!open);
					},
					children: [
						permissionGlyph(currentValue) !== void 0 && (0, react_jsx_runtime.jsx)("span", {
							className: PermissionSelect_module_css_default.triggerIcon,
							"aria-hidden": true,
							children: permissionGlyph(currentValue)
						}),
						(0, react_jsx_runtime.jsx)("span", {
							className: PermissionSelect_module_css_default.triggerLabel,
							children: currentLabel
						}),
						currentBadge !== void 0 && (0, react_jsx_runtime.jsx)("sup", {
							className: PermissionSelect_module_css_default.badge,
							children: currentBadge
						}),
						(0, react_jsx_runtime.jsx)("span", {
							className: clsx(PermissionSelect_module_css_default.chevron, open && PermissionSelect_module_css_default.chevronOpen),
							"aria-hidden": true,
							children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronDownOutline14, {})
						})
					]
				})
			}), confirmation !== null && (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.RiskConfirmation, {
				open: true,
				title: confirmationTitle,
				description: confirmationDescription,
				acknowledgeLabel: confirmationAcknowledge,
				cancelLabel: t("confirm.cancel"),
				closeLabel: t("close"),
				confirmLabel: confirmationEnable,
				acknowledged,
				disabled: locked,
				onAcknowledgedChange: setAcknowledged,
				onCancel: closeConfirmation,
				onConfirm: () => {
					confirmSelection(confirmation);
				}
			})] });
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-permission-presets/src/client/PermissionRow.module.css.mjs
		const css = ".uKcpoW_row{border-bottom:.5px solid var(--dsw-alias-border-l2);align-items:center;gap:8px;padding:16px 0;display:flex}.uKcpoW_rowText{flex-direction:column;flex:1;gap:4px;min-width:0;padding-right:48px;display:flex}.uKcpoW_title{color:var(--dsw-alias-label-primary);font-size:14px;font-weight:400;line-height:22px}.uKcpoW_desc{color:var(--dsw-alias-label-tertiary);font-size:12px;font-weight:400;line-height:18px}.uKcpoW_selector{background:var(--dsw-alias-bg-module-platform);height:36px;font:inherit;color:var(--dsw-alias-label-primary);cursor:pointer;border:none;border-radius:18px;align-items:center;gap:12px;padding:0 14px;font-size:14px;line-height:22px;display:inline-flex}.uKcpoW_selector:hover:not(:disabled){background:var(--dsw-alias-interactive-bg-hover)}.uKcpoW_selector:disabled{cursor:default}.uKcpoW_chevron{flex:none}";
		const tagId = "@deepseek-ai/dsh-client-ui-permission-presets/PermissionRow.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-permission-presets";
			tag.dataset.pluginCss = tagId;
			tag.textContent = css;
			document.head.appendChild(tag);
		}
		var PermissionRow_module_css_default = {
			"chevron": "uKcpoW_chevron",
			"desc": "uKcpoW_desc",
			"row": "uKcpoW_row",
			"rowText": "uKcpoW_rowText",
			"selector": "uKcpoW_selector",
			"title": "uKcpoW_title"
		};
		//#endregion
		//#region lib/types/client/PermissionRow.js
		/**
		* Permission preference row: the default preset for subsequently created
		* sessions. Current-session switches remain on the composer `/permission`
		* control.
		*/
		/**
		* Render the new-session Permission default selector.
		* @param props - composed slot props.
		* @returns the row, or null when the host does not expose permission settings.
		*/
		function PermissionRow({ load, select, usePermission, t }) {
			const state = usePermission((snapshot) => snapshot);
			const [open, setOpen] = (0, react.useState)(false);
			const [confirmingFullAccess, setConfirmingFullAccess] = (0, react.useState)(false);
			const [acknowledged, setAcknowledged] = (0, react.useState)(false);
			(0, react.useEffect)(() => {
				load();
			}, [load]);
			(0, react.useEffect)(() => {
				if (state.writable && state.status !== "unavailable") return;
				setOpen(false);
				setAcknowledged(false);
				setConfirmingFullAccess(false);
			}, [state.status, state.writable]);
			if (state.status === "unavailable") return null;
			const selected = state.options.find((option) => option.id === state.currentValue);
			const busy = state.status === "loading" || state.status === "saving" || confirmingFullAccess;
			const optionLabel = (option) => displayPermissionPreset(option.id, option.label, t);
			const label = selected !== void 0 ? optionLabel(selected) : busy ? t("loading") : t("unavailable");
			const description = state.error ?? t("description");
			return (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsxs)("div", {
				className: PermissionRow_module_css_default.row,
				children: [(0, react_jsx_runtime.jsxs)("div", {
					className: PermissionRow_module_css_default.rowText,
					children: [(0, react_jsx_runtime.jsx)("div", {
						className: PermissionRow_module_css_default.title,
						children: t("title")
					}), (0, react_jsx_runtime.jsx)("div", {
						className: PermissionRow_module_css_default.desc,
						role: state.error === null ? void 0 : "alert",
						children: description
					})]
				}), (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Menu, {
					open,
					onClose: () => {
						setOpen(false);
					},
					items: state.options.map((option) => ({
						id: option.id,
						label: optionLabel(option)
					})),
					selectedId: state.currentValue,
					onSelect: (id) => {
						setOpen(false);
						if (id === state.currentValue) return;
						if (id === "danger-full-access") {
							setAcknowledged(false);
							setConfirmingFullAccess(true);
							return;
						}
						select(id);
					},
					align: "end",
					portal: true,
					anchor: (0, react_jsx_runtime.jsxs)("button", {
						type: "button",
						className: PermissionRow_module_css_default.selector,
						"aria-haspopup": "menu",
						"aria-expanded": open,
						disabled: busy || !state.writable || state.options.length === 0,
						onClick: () => {
							setOpen((value) => !value);
						},
						children: [label, (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronDownOutline14, { className: PermissionRow_module_css_default.chevron })]
					})
				})]
			}), (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.RiskConfirmation, {
				open: confirmingFullAccess,
				title: t("confirm.title"),
				description: t("confirm.description"),
				acknowledgeLabel: t("confirm.acknowledge"),
				cancelLabel: t("confirm.cancel"),
				closeLabel: t("close"),
				confirmLabel: t("confirm.enable"),
				acknowledged,
				disabled: !state.writable || state.status === "saving",
				onAcknowledgedChange: setAcknowledged,
				onCancel: () => {
					setAcknowledged(false);
					setConfirmingFullAccess(false);
				},
				onConfirm: () => {
					setAcknowledged(false);
					setConfirmingFullAccess(false);
					select(FULL_ACCESS_PRESET);
				}
			})] });
		}
		//#endregion
		//#region lib/types/client/settings-store.js
		/**
		* Permission default-settings controller. The permission descriptor comes
		* from the shared describe mirror (the dynamic preset enum lives in the
		* namespace schema, which per-namespace scopes do not carry); writes target
		* only `defaultPreset`, carry the descriptor revision, and fold their answer
		* back into the mirror.
		*/
		/** Permission's settings namespace on the host wire. */
		const PERMISSION_SETTINGS_NS = "permission";
		/**
		* Read the dynamic preset enum encoded by the host's `defaultPreset` schema.
		* @param view - permission namespace descriptor.
		* @param schema - settings schema operations.
		* @returns current value and selectable options.
		*/
		function permissionDefaultOf(view, schema) {
			const value = view.value?.defaultPreset;
			if (typeof value !== "string") throw new Error("permission settings has no defaultPreset value");
			const node = schema.nodeAtPath(schema.rehydrate(view.schema), ["defaultPreset"]);
			if (node === void 0) throw new Error("permission settings schema has no defaultPreset field");
			const options = (node.type === "union" ? node.list ?? [] : [node]).flatMap((candidate) => {
				const choice = candidate;
				if (choice.type !== "const" || typeof choice.value !== "string") return [];
				const described = choice.meta?.description;
				return [{
					id: choice.value,
					label: typeof described === "string" && described.length > 0 ? displayPermissionPreset(choice.value, described) : displayPermissionPreset(choice.value, choice.value)
				}];
			});
			if (options.length === 0 || !options.some((option) => option.id === value)) throw new Error("permission settings schema does not advertise its current preset");
			return {
				currentValue: value,
				options
			};
		}
		/** Controller deriving the row from the shared mirror and writing the default through it. */
		var PermissionPresetSettingsController = class {
			describeFace;
			ctx;
			schema;
			/** Row snapshot consumed through a bound selector hook. */
			store = (0, _deepseek_ai_dsh_client_store.createSnapshotStore)({
				status: "idle",
				error: null,
				writable: false,
				currentValue: "",
				options: [],
				revision: 0
			});
			following;
			saving = false;
			disposed = false;
			/**
			* @param describeFace - the shared mirror's read/fold face (descriptor and schema source).
			* @param ctx - the row plugin's context, whose `remote.settings` namespace
			* carries the `defaultPreset` write.
			* @param schema - settings-owned schema operations.
			*/
			constructor(describeFace, ctx, schema) {
				this.describeFace = describeFace;
				this.ctx = ctx;
				this.schema = schema;
			}
			/**
			* Begin following the mirror (idempotent) and reflect its current answer.
			* @returns settlement once the snapshot reflects the mirror.
			*/
			async load() {
				if (this.disposed) return;
				this.following ??= this.describeFace.subscribe(() => {
					this.derive();
				});
				this.store.update((state) => {
					state.status = "loading";
					state.error = null;
				});
				await this.describeFace.ensure();
				this.derive();
			}
			/**
			* Persist one preset as the default for subsequently created sessions.
			* A selection made while one is already saving is ignored — the row's
			* control is disabled during the save, so this only drops programmatic
			* double-submits rather than user intent.
			* @param preset - advertised preset key.
			* @returns nothing; {@link store} carries success or failure.
			*/
			async select(preset) {
				const state = this.store.getSnapshot();
				const view = this.describeFace.getSnapshot().view?.namespaces.find((entry) => entry.ns === PERMISSION_SETTINGS_NS);
				if (view === void 0 || !state.writable || this.saving) return;
				this.saving = true;
				this.store.update((draft) => {
					draft.status = "saving";
					draft.error = null;
				});
				let response;
				try {
					response = await this.ctx.remote.settings.mutate(PERMISSION_SETTINGS_NS, [{
						op: "set",
						path: ["defaultPreset"],
						value: preset
					}], view.revision);
				} finally {
					this.saving = false;
				}
				if (this.disposed) return;
				if (!response.ok) {
					this.fail(response.error);
					return;
				}
				this.describeFace.acceptView(response.value);
			}
			/** Stop following the mirror; later publishes leave the snapshot alone. */
			dispose() {
				this.disposed = true;
				this.following?.();
				this.following = void 0;
			}
			derive() {
				if (this.disposed || this.saving) return;
				const mirrored = this.describeFace.getSnapshot();
				if (mirrored.status === "unavailable") {
					this.store.update((state) => {
						state.status = "unavailable";
						state.writable = false;
						state.currentValue = "";
						state.options = [];
					});
					return;
				}
				if (mirrored.view === void 0) {
					if (mirrored.error !== null) this.fail(new Error(mirrored.error));
					return;
				}
				const view = mirrored.view.namespaces.find((entry) => entry.ns === PERMISSION_SETTINGS_NS);
				if (view === void 0) {
					this.store.update((state) => {
						state.status = "unavailable";
						state.writable = false;
						state.currentValue = "";
						state.options = [];
					});
					return;
				}
				try {
					const resolved = permissionDefaultOf(view, this.schema);
					const { writable } = mirrored.view;
					this.store.update((state) => {
						state.status = "ready";
						state.error = null;
						state.writable = writable;
						state.currentValue = resolved.currentValue;
						state.options = resolved.options;
						state.revision = view.revision;
					});
				} catch (error) {
					this.fail(error);
				}
			}
			fail(error) {
				this.store.update((state) => {
					state.status = "error";
					state.error = error instanceof Error ? error.message : String(error);
				});
			}
		};
		//#endregion
		//#region lib/types/client/index.js
		/** Required services (cordis fiber inject). */
		const inject = [
			"commandUi",
			"connection",
			"sessions",
			"slots",
			"locale",
			"remote",
			"remote.permissionPresets",
			"remote.settings",
			"settingsScope",
			"settingsSchema"
		];
		/** Read one session's current permissions projection value (undefined = capability absent). */
		function selectionOf(session) {
			return session?.projections.faceOf("permissions").getSnapshot();
		}
		/** Join the process catalog with one Session's current value. */
		function optionsOf(catalog, currentValue, t) {
			return catalog.options.map((option) => ({
				id: option.value,
				label: option.value === "auto" ? t("auto.label") : displayPermissionPreset(option.value, option.name, t),
				...option.value === "auto" ? { badge: t("auto.badge") } : {},
				...option.value === "auto" ? { detail: t("auto.description") } : option.description !== void 0 ? { detail: option.description } : {},
				...option.value === currentValue ? { active: true } : {},
				...option.value === "danger-full-access" || option.value === "auto" ? { confirmation: {
					title: t(option.value === "auto" ? "auto.confirm.title" : "confirm.title"),
					description: t(option.value === "auto" ? "auto.confirm.description" : "confirm.description"),
					acknowledgeLabel: t(option.value === "auto" ? "auto.confirm.acknowledge" : "confirm.acknowledge"),
					cancelLabel: t("confirm.cancel"),
					confirmLabel: t(option.value === "auto" ? "auto.confirm.enable" : "confirm.enable")
				} } : {}
			}));
		}
		/**
		* Client plugin body: register the /permission popup picker over the
		* permissions projection.
		* @param ctx - client root context.
		*/
		function apply(ctx) {
			const command = ctx.get("commandUi");
			const sessions = ctx.sessions;
			ctx.effect(() => ctx.locale.register(PERMISSION_ACCESS_NS, {
				zh: accessZh,
				en: accessEn
			}), "ui-permission: current-session dictionaries");
			const t = ctx.locale.bind(PERMISSION_ACCESS_NS);
			const sessionFor = (session) => sessions.binding(session.sessionId)?.session;
			const submit = async (sessionId, preset) => {
				const live = sessions.binding(sessionId)?.session;
				if (live === void 0) throw new Error("this session is not materialized yet");
				const result = await live.command(`/permission ${preset}`);
				if (!result.ok) throw new Error(`permission switch failed: ${result.error.code}: ${result.error.message}`);
				if (!result.value.matched) throw new Error("the host offers no /permission command");
				return true;
			};
			const catalog = new PermissionCatalogDirectory(ctx);
			ctx.effect(() => () => {
				catalog.dispose();
			}, "ui-permission: process catalog directory");
			ctx.effect(() => catalog.invalidations.subscribe(() => {
				command.dismiss("permission");
			}), "ui-permission: dismiss stale slash choices");
			ctx.effect(() => ctx.locale.register("settings.permission", {
				zh,
				en
			}), "ui-permission: settings row dictionaries");
			const controller = new PermissionPresetSettingsController(ctx.settingsScope.describe(), ctx, ctx.settingsSchema);
			const load = () => controller.load();
			const select = (preset) => controller.select(preset);
			const injected = () => ({
				hooks: { permission: controller.store },
				load,
				select
			});
			ctx.effect(() => () => {
				controller.dispose();
			}, "ui-permission: settings row directory");
			ctx.slots.inject("settings.general.item", () => ctx.slots.register({
				name: "settings.general.item",
				id: "permission",
				order: -20,
				locale: "settings.permission",
				inject: injected
			}, PermissionRow));
			ctx.slots.inject("conversation.input.permission", () => ctx.slots.register({
				name: "conversation.input.permission",
				locale: PERMISSION_ACCESS_NS,
				inject: (sessionId) => ({
					hooks: { permissionCatalog: catalog.store },
					select: (preset) => submit(sessionId, preset)
				})
			}, PermissionSelect));
			ctx.effect(() => command.decorate({
				name: "permission",
				available: (session) => selectionOf(sessionFor(session)) !== void 0,
				ui: {
					kind: "popupSelect",
					options: async (session) => {
						const selection = selectionOf(sessionFor(session));
						if (selection === void 0) throw new Error("permission presets are not available on this host");
						return optionsOf(await catalog.load(), selection.currentValue, t);
					},
					onSelect: (option, session) => submit(session.sessionId, option.id).then(() => void 0)
				}
			}), "ui-permission: /permission decoration");
		}
		//#endregion
		exports.apply = apply;
		exports.inject = inject;
		return module.exports;
	}
});

//# sourceMappingURL=client.js.map