window.__ModuleLoader__.load({
	id: "@deepseek-ai/dsh-client-ui-settings-plugins",
	factory: (require) => {
		var module = { exports: {} };
		var exports = module.exports;
		Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
		let _deepseek_ai_dsh_client_ui_slots = require("@deepseek-ai/dsh-client-ui-slots");
		let react_jsx_runtime = require("react/jsx-runtime");
		let react = require("react");
		let _deepseek_ai_dsh_client_ui_primitives = require("@deepseek-ai/dsh-client-ui-primitives");
		let _deepseek_ai_dsh_client_store = require("@deepseek-ai/dsh-client-store");
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-settings-plugins/src/client/fields.module.css.mjs
		const css$5 = ".fGZdUq_field{flex-direction:column;gap:6px;padding:12px 0;display:flex}.fGZdUq_field+.fGZdUq_field{border-top:.5px solid var(--dsw-alias-border-l2)}.fGZdUq_head{align-items:center;gap:8px;display:flex}.fGZdUq_label{min-width:0;color:var(--dsw-alias-label-primary);flex:1;font-size:13px;font-weight:500;line-height:1.5}.fGZdUq_labelGroup{flex:1;align-items:center;gap:4px;min-width:0;display:flex}.fGZdUq_labelGroup>.fGZdUq_label{flex:0 auto}.fGZdUq_helpButton{width:24px;height:24px;color:var(--dsw-alias-label-tertiary);cursor:pointer;background:0 0;border:0;border-radius:6px;flex:none;justify-content:center;align-items:center;padding:0;display:inline-flex}.fGZdUq_helpButton:hover,.fGZdUq_helpButton[aria-expanded=true]{background:var(--dsw-alias-bg-layer-4);color:var(--dsw-alias-label-secondary)}.fGZdUq_helpButton:focus-visible{outline:2px solid var(--dsw-alias-brand-primary);outline-offset:1px}.fGZdUq_help{color:var(--dsw-alias-label-secondary);padding:10px 0 0;font-size:12px;line-height:1.6}.fGZdUq_help>p{margin:0}.fGZdUq_help>p+p{margin-top:8px}.fGZdUq_badges{align-items:center;gap:8px;display:inline-flex}.fGZdUq_reset{font:inherit;color:var(--dsw-alias-label-secondary);cursor:pointer;background:0 0;border:none;padding:0;font-size:12px;line-height:1.5}.fGZdUq_reset:hover:not(:disabled){color:var(--dsw-alias-label-primary)}.fGZdUq_reset:disabled{cursor:default}.fGZdUq_input{border:.5px solid var(--dsw-alias-border-l4);background:var(--dsw-alias-bg-layer-3);height:34px;font:inherit;color:var(--dsw-alias-label-primary);border-radius:8px;padding:0 12px;font-size:13px;line-height:1.5}.fGZdUq_input:focus-visible{border-color:var(--dsw-alias-brand-primary);outline:none}.fGZdUq_input:disabled{color:var(--dsw-alias-label-tertiary);cursor:default}.fGZdUq_input[aria-invalid=true]{border-color:var(--dsw-alias-state-error-primary)}.fGZdUq_invalid{color:var(--dsw-alias-state-error-primary);margin:0;font-size:12px;line-height:1.5}.fGZdUq_hint{color:var(--dsw-alias-label-tertiary);margin:0;font-size:12px;line-height:1.5}";
		const tagId$5 = "@deepseek-ai/dsh-client-ui-settings-plugins/fields.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$5) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-settings-plugins";
			tag.dataset.pluginCss = tagId$5;
			tag.textContent = css$5;
			document.head.appendChild(tag);
		}
		var fields_module_css_default = {
			"badges": "fGZdUq_badges",
			"field": "fGZdUq_field",
			"head": "fGZdUq_head",
			"help": "fGZdUq_help",
			"helpButton": "fGZdUq_helpButton",
			"hint": "fGZdUq_hint",
			"input": "fGZdUq_input",
			"invalid": "fGZdUq_invalid",
			"label": "fGZdUq_label",
			"labelGroup": "fGZdUq_labelGroup",
			"reset": "fGZdUq_reset"
		};
		//#endregion
		//#region lib/types/client/fields.js
		/**
		* Hand-written controls for the plugin configuration forms. Each renders one
		* field's label, its staged text, whether saving would leave an override, and
		* — when one stands — the reset that stages a clear back to the composition
		* layer. Nothing here writes: a control reports what the user typed, and the
		* card's save is the single point where a draft becomes a document mutation.
		*/
		/**
		* A staged value field. `numeric` only hints the keypad: which drafts a field
		* accepts is decided by its spec, so the control never silently rewrites what
		* the user typed.
		* @param props - the field's copy, its staged text, and the edit actions.
		* @returns the labelled control.
		*/
		function ValueField(props) {
			const [helpOpen, setHelpOpen] = (0, react.useState)(false);
			const helpId = `${props.id}-help`;
			const messageId = `${props.id}-message`;
			const hasMessage = props.invalid || Boolean(props.hint);
			const description = [hasMessage ? messageId : "", helpOpen ? helpId : ""].filter(Boolean).join(" ");
			return (0, react_jsx_runtime.jsxs)("div", {
				className: fields_module_css_default.field,
				children: [
					(0, react_jsx_runtime.jsxs)("div", {
						className: fields_module_css_default.head,
						children: [(0, react_jsx_runtime.jsxs)("div", {
							className: fields_module_css_default.labelGroup,
							children: [(0, react_jsx_runtime.jsx)("label", {
								className: fields_module_css_default.label,
								htmlFor: props.id,
								children: props.label
							}), props.help !== void 0 ? (0, react_jsx_runtime.jsx)("button", {
								type: "button",
								className: fields_module_css_default.helpButton,
								"aria-label": props.help.label,
								"aria-expanded": helpOpen,
								"aria-controls": helpId,
								onClick: () => {
									setHelpOpen(!helpOpen);
								},
								children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconInfoOutline14, { size: 12 })
							}) : null]
						}), props.overridden ? (0, react_jsx_runtime.jsxs)("span", {
							className: fields_module_css_default.badges,
							children: [(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tag, {
								tone: "neutral",
								children: props.overriddenLabel
							}), (0, react_jsx_runtime.jsx)("button", {
								type: "button",
								className: fields_module_css_default.reset,
								disabled: props.disabled,
								onClick: props.onReset,
								children: props.resetLabel
							})]
						}) : null]
					}),
					(0, react_jsx_runtime.jsx)("input", {
						id: props.id,
						className: fields_module_css_default.input,
						type: "text",
						...props.numeric === true ? { inputMode: "numeric" } : {},
						...props.invalid ? { "aria-invalid": true } : {},
						"aria-describedby": description || void 0,
						value: props.text,
						placeholder: props.placeholder ?? "",
						disabled: props.disabled,
						onChange: (event) => {
							props.onEdit(event.target.value);
						}
					}),
					hasMessage ? (0, react_jsx_runtime.jsx)("p", {
						id: messageId,
						className: props.invalid ? fields_module_css_default.invalid : fields_module_css_default.hint,
						children: props.invalid ? props.invalidLabel : props.hint
					}) : null,
					props.help !== void 0 && helpOpen ? (0, react_jsx_runtime.jsx)("div", {
						id: helpId,
						className: fields_module_css_default.help,
						role: "region",
						"aria-label": props.help.label,
						children: props.help.content
					}) : null
				]
			});
		}
		/**
		* A write-only credential control. The value never rides a response, so the
		* control reports only whether one is configured and starts blank; a blank
		* draft writes nothing, which keeps the stored key rather than clearing it.
		* @param props - the field's copy, its staged text, and the configured state.
		* @returns the labelled control.
		*/
		function SecretField(props) {
			return (0, react_jsx_runtime.jsxs)("div", {
				className: fields_module_css_default.field,
				children: [
					(0, react_jsx_runtime.jsxs)("div", {
						className: fields_module_css_default.head,
						children: [(0, react_jsx_runtime.jsx)("label", {
							className: fields_module_css_default.label,
							htmlFor: props.id,
							children: props.label
						}), (0, react_jsx_runtime.jsx)("span", {
							className: fields_module_css_default.badges,
							children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Tag, {
								tone: props.configured ? "neutral" : "quiet",
								children: props.stateLabel
							})
						})]
					}),
					(0, react_jsx_runtime.jsx)("input", {
						id: props.id,
						className: fields_module_css_default.input,
						type: "password",
						autoComplete: "off",
						value: props.text,
						disabled: props.disabled,
						onChange: (event) => {
							props.onEdit(event.target.value);
						}
					}),
					(0, react_jsx_runtime.jsx)("p", {
						className: fields_module_css_default.hint,
						children: props.hint
					})
				]
			});
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-settings-plugins/src/client/PluginConfigForm.module.css.mjs
		const css$4 = ".vo1DxG_form{flex-direction:column;display:flex}.vo1DxG_readOnly,.vo1DxG_unavailable{color:var(--dsw-alias-label-tertiary);margin:0 0 12px;font-size:12px;line-height:1.5}.vo1DxG_footer{align-items:center;gap:8px;padding-top:16px;display:flex}.vo1DxG_failed{min-width:0;color:var(--dsw-alias-label-error);flex:1;margin:0;font-size:12px;line-height:1.5}.vo1DxG_save{appearance:none;font:inherit;cursor:pointer;background:var(--dsw-alias-label-primary);color:var(--dsw-alias-bg-layer-3);border:1px solid #0000;border-radius:8px;padding:5px 14px;font-size:13px;line-height:1.5}.vo1DxG_save:disabled{opacity:.4;cursor:default}.vo1DxG_save:focus-visible{outline:2px solid var(--dsw-alias-brand-primary);outline-offset:1px}";
		const tagId$4 = "@deepseek-ai/dsh-client-ui-settings-plugins/PluginConfigForm.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$4) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-settings-plugins";
			tag.dataset.pluginCss = tagId$4;
			tag.textContent = css$4;
			document.head.appendChild(tag);
		}
		var PluginConfigForm_module_css_default = {
			"failed": "vo1DxG_failed",
			"footer": "vo1DxG_footer",
			"form": "vo1DxG_form",
			"readOnly": "vo1DxG_readOnly",
			"save": "vo1DxG_save",
			"unavailable": "vo1DxG_unavailable"
		};
		//#endregion
		//#region lib/types/client/PluginConfigForm.js
		/**
		* One plugin's configuration form as its page on the Plugins page shows it:
		* the read-only notice when the deployment stores settings read-only, the
		* plugin's controls, and the save that writes every staged edit. The page
		* draws the plugin's title and one-liner itself.
		*
		* Only a save writes. Leaving the page drops every staged edit, so the form
		* discards on unmount and offers no discard control. A form whose namespace
		* the Host stopped serving says so in place of its controls rather than
		* showing fields nothing would accept.
		*/
		/**
		* Render one plugin's configuration form.
		* @param props - the form state, its controls, and the save and discard actions.
		* @returns the form, or the unavailable line while the namespace is not served.
		*/
		function PluginConfigForm(props) {
			const { state } = props;
			const discard = (0, react.useRef)(props.onDiscard);
			discard.current = props.onDiscard;
			(0, react.useEffect)(() => () => {
				discard.current();
			}, []);
			if (!state.available) return (0, react_jsx_runtime.jsx)("p", {
				className: PluginConfigForm_module_css_default.unavailable,
				role: "status",
				children: props.t("unavailable")
			});
			const blocked = !state.dirty || state.invalid || state.saving;
			return (0, react_jsx_runtime.jsxs)("div", {
				className: PluginConfigForm_module_css_default.form,
				children: [
					!state.writable ? (0, react_jsx_runtime.jsx)("p", {
						className: PluginConfigForm_module_css_default.readOnly,
						role: "status",
						children: props.t("readOnly")
					}) : null,
					props.children,
					(0, react_jsx_runtime.jsxs)("div", {
						className: PluginConfigForm_module_css_default.footer,
						children: [state.failed ? (0, react_jsx_runtime.jsx)("p", {
							className: PluginConfigForm_module_css_default.failed,
							role: "status",
							children: props.t("saveFailed")
						}) : null, (0, react_jsx_runtime.jsx)("button", {
							type: "button",
							className: PluginConfigForm_module_css_default.save,
							disabled: blocked,
							onClick: props.onSave,
							children: props.t(state.saving ? "saving" : "save")
						})]
					})
				]
			});
		}
		//#endregion
		//#region lib/types/client/AgentLoopCard.js
		/**
		* Render the agent loop's one-liner or its configuration form, as the Plugins page asks.
		* @param props - the view asked for, locale copy, the form snapshot, and its actions.
		* @returns the one-liner, or the form.
		*/
		function AgentLoopCard(props) {
			const { t } = props;
			const state = props.useAgentLoopCard((snapshot) => snapshot);
			if (props.view === "summary") return t("agentLoopDescription");
			return (0, react_jsx_runtime.jsx)(PluginConfigForm, {
				t,
				state,
				onSave: props.save,
				onDiscard: props.discard,
				children: (0, react_jsx_runtime.jsx)(ValueField, {
					id: "plugin-config-agent-loop-parallel",
					label: t("agentLoopMaxParallel"),
					hint: t("agentLoopMaxParallelHint"),
					overriddenLabel: t("overridden"),
					resetLabel: t("reset"),
					invalidLabel: t("invalidNumber"),
					numeric: true,
					disabled: !state.writable,
					...state.maxParallelToolCalls,
					onEdit: (text) => {
						props.edit("maxParallelToolCalls", text);
					},
					onReset: () => {
						props.resetField("maxParallelToolCalls");
					}
				})
			});
		}
		//#endregion
		//#region lib/types/client/BashCard.js
		/**
		* Render the shell plugin's one-liner or its configuration form, as the Plugins page asks.
		* @param props - the view asked for, locale copy, the form snapshot, and its actions.
		* @returns the one-liner, or the form.
		*/
		function BashCard(props) {
			const { t } = props;
			const state = props.useBashCard((snapshot) => snapshot);
			if (props.view === "summary") return t("bashDescription");
			const disabled = !state.writable;
			return (0, react_jsx_runtime.jsxs)(PluginConfigForm, {
				t,
				state,
				onSave: props.save,
				onDiscard: props.discard,
				children: [(0, react_jsx_runtime.jsx)(ValueField, {
					id: "plugin-config-bash-timeout",
					label: t("bashTimeoutMs"),
					hint: t("bashTimeoutMsHint"),
					overriddenLabel: t("overridden"),
					resetLabel: t("reset"),
					invalidLabel: t("invalidNumber"),
					numeric: true,
					disabled,
					...state.timeoutMs,
					onEdit: (text) => {
						props.edit("timeoutMs", text);
					},
					onReset: () => {
						props.resetField("timeoutMs");
					}
				}), (0, react_jsx_runtime.jsx)(ValueField, {
					id: "plugin-config-bash-output",
					label: t("bashMaxOutputBytes"),
					hint: t("bashMaxOutputBytesHint"),
					overriddenLabel: t("overridden"),
					resetLabel: t("reset"),
					invalidLabel: t("invalidNumber"),
					numeric: true,
					disabled,
					...state.maxOutputBytes,
					onEdit: (text) => {
						props.edit("maxOutputBytes", text);
					},
					onReset: () => {
						props.resetField("maxOutputBytes");
					}
				})]
			});
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-settings-plugins/src/client/PluginsSettingsSection.module.css.mjs
		const css$3 = ".HJU66G_section{max-width:760px;color:var(--dsw-alias-label-primary);flex-direction:column;gap:12px;display:flex}.HJU66G_heading{margin:0;font-size:18px;font-weight:600}.HJU66G_intro{color:var(--dsw-alias-label-tertiary);margin:0;font-size:13px}.HJU66G_tabs{border-bottom:.5px solid var(--dsw-alias-border-l2);align-items:flex-end;gap:22px;margin-top:2px;display:flex}.HJU66G_tab{color:var(--dsw-alias-label-tertiary);font:inherit;cursor:pointer;background:0 0;border:0;padding:7px 1px 9px;font-size:13px;line-height:20px;position:relative}.HJU66G_tab:hover,.HJU66G_tab[data-active=true]{color:var(--dsw-alias-label-primary)}.HJU66G_tab[data-active=true]:after,.HJU66G_tab:focus-visible:after{background:var(--dsw-alias-label-primary);content:\"\";border-radius:2px 2px 0 0;height:2px;position:absolute;bottom:-1px;left:0;right:0}.HJU66G_tab:focus-visible{outline:2px solid var(--dsw-alias-state-business-primary);outline-offset:2px;color:var(--dsw-alias-label-primary);border-radius:2px}.HJU66G_panel{min-width:0;padding-top:2px}.HJU66G_cards{flex-direction:column;gap:10px;margin:0;padding:0;list-style:none;display:flex}.HJU66G_empty{color:var(--dsw-alias-label-tertiary);margin:0;font-size:13px}.HJU66G_configurable{flex-direction:column;gap:10px;display:flex}.HJU66G_presetSettings{flex-direction:column;gap:8px;display:flex}.HJU66G_presetSettingsTitle{margin:0;font-size:15px;font-weight:600;line-height:22px}";
		const tagId$3 = "@deepseek-ai/dsh-client-ui-settings-plugins/PluginsSettingsSection.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$3) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-settings-plugins";
			tag.dataset.pluginCss = tagId$3;
			tag.textContent = css$3;
			document.head.appendChild(tag);
		}
		var PluginsSettingsSection_module_css_default = {
			"cards": "HJU66G_cards",
			"configurable": "HJU66G_configurable",
			"empty": "HJU66G_empty",
			"heading": "HJU66G_heading",
			"intro": "HJU66G_intro",
			"panel": "HJU66G_panel",
			"presetSettings": "HJU66G_presetSettings",
			"presetSettingsTitle": "HJU66G_presetSettingsTitle",
			"section": "HJU66G_section",
			"tab": "HJU66G_tab",
			"tabs": "HJU66G_tabs"
		};
		//#endregion
		//#region lib/types/client/PluginsSettingsSection.js
		/** Plugins settings section: localized tabs around feature-owned pages. */
		/** Render one Plugins page whose contents arrive from feature-owned tabs; one contribution shows as the page itself. */
		function PluginsSettingsSection({ t, renderSlot, useTabs }) {
			const tabsId = (0, react.useId)();
			const tabRefs = (0, react.useRef)([]);
			const rows = useTabs((value) => value);
			const [activeId, setActiveId] = (0, react.useState)();
			const [visitedIds, setVisitedIds] = (0, react.useState)(() => /* @__PURE__ */ new Set());
			const active = rows.find((row) => row.id === activeId)?.id ?? rows[0]?.id;
			const single = rows.length === 1 ? rows[0] : void 0;
			(0, react.useEffect)(() => {
				if (active === void 0) return;
				setVisitedIds((previous) => {
					if (previous.has(active)) return previous;
					return new Set([...previous, active]);
				});
			}, [active]);
			return (0, react_jsx_runtime.jsxs)("div", {
				className: PluginsSettingsSection_module_css_default.section,
				children: [
					(0, react_jsx_runtime.jsx)("h2", {
						className: PluginsSettingsSection_module_css_default.heading,
						children: t("title")
					}),
					(0, react_jsx_runtime.jsx)("p", {
						className: PluginsSettingsSection_module_css_default.intro,
						children: t("intro")
					}),
					rows.length === 0 ? (0, react_jsx_runtime.jsx)("p", {
						className: PluginsSettingsSection_module_css_default.empty,
						children: t("empty")
					}) : single !== void 0 ? (0, react_jsx_runtime.jsx)("div", {
						className: PluginsSettingsSection_module_css_default.panel,
						children: renderSlot("settings.plugins.tab", {}, { only: single.id })
					}) : (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)("div", {
						className: PluginsSettingsSection_module_css_default.tabs,
						role: "tablist",
						"aria-label": t("tabs"),
						children: rows.map((row, index) => {
							const selected = row.id === active;
							return (0, react_jsx_runtime.jsx)("button", {
								ref: (element) => {
									tabRefs.current[index] = element;
								},
								id: `${tabsId}-tab-${row.id}`,
								type: "button",
								role: "tab",
								className: PluginsSettingsSection_module_css_default.tab,
								"aria-selected": selected,
								"aria-controls": `${tabsId}-panel-${row.id}`,
								"data-active": selected ? "true" : void 0,
								tabIndex: selected ? 0 : -1,
								onClick: () => {
									setActiveId(row.id);
								},
								onKeyDown: (event) => {
									let nextIndex;
									switch (event.key) {
										case "ArrowRight":
											nextIndex = (index + 1) % rows.length;
											break;
										case "ArrowLeft":
											nextIndex = (index - 1 + rows.length) % rows.length;
											break;
										case "Home":
											nextIndex = 0;
											break;
										case "End":
											nextIndex = rows.length - 1;
											break;
										default: return;
									}
									event.preventDefault();
									const nextRow = rows[nextIndex];
									const nextTab = tabRefs.current[nextIndex];
									setActiveId(nextRow.id);
									nextTab.focus();
								},
								children: row.label
							}, row.id);
						})
					}), rows.filter((row) => row.id === active || visitedIds.has(row.id)).map((row) => {
						const selected = row.id === active;
						return (0, react_jsx_runtime.jsx)("div", {
							id: `${tabsId}-panel-${row.id}`,
							className: PluginsSettingsSection_module_css_default.panel,
							role: "tabpanel",
							"aria-labelledby": `${tabsId}-tab-${row.id}`,
							hidden: !selected,
							children: renderSlot("settings.plugins.tab", {}, { only: row.id })
						}, row.id);
					})] })
				]
			});
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-settings-plugins/src/client/SubagentLimitsFields.module.css.mjs
		const css$2 = ".eWUCuW_limits{grid-template-columns:repeat(auto-fit,minmax(min(220px,100%),1fr));gap:16px;display:grid}.eWUCuW_limit{min-width:0}.eWUCuW_limit input{font-variant-numeric:tabular-nums;min-width:0}.eWUCuW_depthTable{border-collapse:collapse;border-block:.5px solid var(--dsw-alias-border-l2);width:100%;font:inherit;text-align:left;margin:8px 0}.eWUCuW_depthTable th,.eWUCuW_depthTable td{vertical-align:top;padding:6px 0}.eWUCuW_depthTable th{font-variant-numeric:tabular-nums;width:24px;color:var(--dsw-alias-label-primary);padding-right:8px;font-weight:500}.eWUCuW_depthTable tr+tr{border-top:.5px solid var(--dsw-alias-border-l2)}";
		const tagId$2 = "@deepseek-ai/dsh-client-ui-settings-plugins/SubagentLimitsFields.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$2) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-settings-plugins";
			tag.dataset.pluginCss = tagId$2;
			tag.textContent = css$2;
			document.head.appendChild(tag);
		}
		var SubagentLimitsFields_module_css_default = {
			"depthTable": "eWUCuW_depthTable",
			"limit": "eWUCuW_limit",
			"limits": "eWUCuW_limits"
		};
		//#endregion
		//#region lib/types/client/SubagentLimitsFields.js
		/**
		* Render the depth and capacity fields with their original validation and reset behavior.
		* @param props - Locale, staged fields, and edit callbacks.
		* @returns Two responsive fields and their application rules.
		*/
		function SubagentLimitsFields(props) {
			const { t, state } = props;
			return (0, react_jsx_runtime.jsx)(react_jsx_runtime.Fragment, { children: (0, react_jsx_runtime.jsxs)("div", {
				className: SubagentLimitsFields_module_css_default.limits,
				children: [(0, react_jsx_runtime.jsx)("div", {
					className: SubagentLimitsFields_module_css_default.limit,
					children: (0, react_jsx_runtime.jsx)(ValueField, {
						id: "plugin-config-subagent-depth",
						label: t("subagentMaxDepth"),
						help: {
							label: t("subagentDepthHelpLabel"),
							content: (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [
								(0, react_jsx_runtime.jsx)("p", { children: t("subagentDepthHelp") }),
								(0, react_jsx_runtime.jsx)("table", {
									className: SubagentLimitsFields_module_css_default.depthTable,
									"aria-label": t("subagentDepthHelpLabel"),
									children: (0, react_jsx_runtime.jsxs)("tbody", { children: [(0, react_jsx_runtime.jsxs)("tr", { children: [(0, react_jsx_runtime.jsx)("th", {
										scope: "row",
										children: 0
									}), (0, react_jsx_runtime.jsx)("td", { children: t("subagentDepthZero") })] }), (0, react_jsx_runtime.jsxs)("tr", { children: [(0, react_jsx_runtime.jsx)("th", {
										scope: "row",
										children: 1
									}), (0, react_jsx_runtime.jsx)("td", { children: t("subagentDepthOne") })] })] })
								}),
								(0, react_jsx_runtime.jsx)("p", { children: t("subagentDepthOverride") })
							] })
						},
						overriddenLabel: t("overridden"),
						resetLabel: t("reset"),
						invalidLabel: t("subagentDepthInvalid"),
						numeric: true,
						disabled: !state.writable || state.saving,
						...state.maxDepth,
						onEdit: (text) => {
							props.edit("maxDepth", text);
						},
						onReset: () => {
							props.resetField("maxDepth");
						}
					})
				}), (0, react_jsx_runtime.jsx)("div", {
					className: SubagentLimitsFields_module_css_default.limit,
					children: (0, react_jsx_runtime.jsx)(ValueField, {
						id: "plugin-config-subagent-capacity",
						label: t("subagentMaxActive"),
						help: {
							label: t("subagentCapacityHelpLabel"),
							content: (0, react_jsx_runtime.jsx)("p", { children: t("subagentCapacityHelp") })
						},
						overriddenLabel: t("overridden"),
						resetLabel: t("reset"),
						invalidLabel: t("subagentCapacityInvalid"),
						numeric: true,
						disabled: !state.writable || state.saving,
						...state.maxActiveSubagents,
						onEdit: (text) => {
							props.edit("maxActiveSubagents", text);
						},
						onReset: () => {
							props.resetField("maxActiveSubagents");
						}
					})
				})]
			}) });
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-settings-plugins/src/client/SubagentModelSelectionFields.module.css.mjs
		const css$1 = "._0kztlq_permission{gap:6px;padding:12px 0;display:grid}._0kztlq_toggleRow{color:var(--dsw-alias-label-primary);justify-content:space-between;align-items:flex-start;gap:16px;font-size:13px;line-height:1.5;display:flex}._0kztlq_toggleLabel{flex:1;min-width:0}._0kztlq_selection{gap:10px;display:grid}._0kztlq_hint,._0kztlq_notice,._0kztlq_invalid,._0kztlq_conflict{margin:0;font-size:12px;line-height:1.5}._0kztlq_hint,._0kztlq_notice{color:var(--dsw-alias-label-tertiary)}._0kztlq_invalid,._0kztlq_conflict{color:var(--dsw-alias-state-error-primary)}._0kztlq_catalogError{color:var(--dsw-alias-state-error-primary);justify-content:space-between;align-items:center;gap:12px;font-size:12px;display:flex}._0kztlq_catalogError button{color:var(--dsw-alias-brand-primary);cursor:pointer;background:0 0;border:0;padding:0}._0kztlq_models{border:.5px solid var(--dsw-alias-border-l4);border-radius:8px;gap:6px;min-width:0;max-height:280px;margin:0;padding:10px;display:grid;overflow:auto}._0kztlq_models legend{color:var(--dsw-alias-label-secondary);padding:0 4px;font-size:12px}._0kztlq_modelGroup{gap:6px;display:grid}._0kztlq_modelGroup+._0kztlq_modelGroup{border-top:.5px solid var(--dsw-alias-border-l3);margin-top:4px;padding-top:10px}._0kztlq_providerName{color:var(--dsw-alias-label-tertiary);padding:0 6px;font-size:11px;font-weight:500}._0kztlq_model{cursor:pointer;border-radius:6px;grid-template-columns:auto minmax(0,1fr) auto;align-items:center;gap:8px;min-width:0;padding:6px;display:grid}._0kztlq_model:hover{background:var(--dsw-alias-bg-layer-4)}._0kztlq_modelName,._0kztlq_route{text-overflow:ellipsis;white-space:nowrap;display:block;overflow:hidden}._0kztlq_modelName{color:var(--dsw-alias-label-primary);font-size:13px}._0kztlq_route{color:var(--dsw-alias-label-tertiary);margin-top:2px;font-size:11px}._0kztlq_unavailable{color:var(--dsw-alias-label-tertiary);font-size:11px}";
		const tagId$1 = "@deepseek-ai/dsh-client-ui-settings-plugins/SubagentModelSelectionFields.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$1) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-settings-plugins";
			tag.dataset.pluginCss = tagId$1;
			tag.textContent = css$1;
			document.head.appendChild(tag);
		}
		var SubagentModelSelectionFields_module_css_default = {
			"catalogError": "_0kztlq_catalogError",
			"conflict": "_0kztlq_conflict",
			"hint": "_0kztlq_hint",
			"invalid": "_0kztlq_invalid",
			"model": "_0kztlq_model",
			"modelGroup": "_0kztlq_modelGroup",
			"modelName": "_0kztlq_modelName",
			"models": "_0kztlq_models",
			"notice": "_0kztlq_notice",
			"permission": "_0kztlq_permission",
			"providerName": "_0kztlq_providerName",
			"route": "_0kztlq_route",
			"selection": "_0kztlq_selection",
			"toggleLabel": "_0kztlq_toggleLabel",
			"toggleRow": "_0kztlq_toggleRow",
			"unavailable": "_0kztlq_unavailable"
		};
		//#endregion
		//#region lib/types/client/SubagentModelSelectionFields.js
		/** User control for model-selectable subagent delegation in new sessions. */
		/**
		* Render the default-off preference and its exact adapter-route choices.
		* @param props - locale copy, the card snapshot, and its toggle action.
		* @returns the model permission and route choices inside the shared card.
		*/
		function SubagentModelSelectionFields(props) {
			const { t, state } = props;
			const availableGroups = /* @__PURE__ */ new Map();
			const unavailable = [];
			for (const candidate of state.candidates) {
				if (!candidate.available) {
					unavailable.push(candidate);
					continue;
				}
				const group = availableGroups.get(candidate.provider);
				if (group === void 0) availableGroups.set(candidate.provider, {
					providerName: candidate.providerName,
					candidates: [candidate]
				});
				else group.candidates.push(candidate);
			}
			const renderCandidate = (candidate) => (0, react_jsx_runtime.jsxs)("label", {
				className: SubagentModelSelectionFields_module_css_default.model,
				children: [
					(0, react_jsx_runtime.jsx)("input", {
						type: "checkbox",
						checked: candidate.selected,
						disabled: !state.writable || state.saving,
						onChange: () => {
							props.toggleModel(candidate.key);
						}
					}),
					(0, react_jsx_runtime.jsxs)("span", { children: [(0, react_jsx_runtime.jsx)("span", {
						className: SubagentModelSelectionFields_module_css_default.modelName,
						children: candidate.modelName
					}), (0, react_jsx_runtime.jsx)("span", {
						className: SubagentModelSelectionFields_module_css_default.route,
						children: `${candidate.providerName} · ${candidate.provider}/${candidate.model}`
					})] }),
					!candidate.available ? (0, react_jsx_runtime.jsx)("span", {
						className: SubagentModelSelectionFields_module_css_default.unavailable,
						children: t("subagentModelSelectionUnavailable")
					}) : null
				]
			}, candidate.key);
			return (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [
				(0, react_jsx_runtime.jsxs)("div", {
					className: SubagentModelSelectionFields_module_css_default.permission,
					children: [(0, react_jsx_runtime.jsxs)("div", {
						className: SubagentModelSelectionFields_module_css_default.toggleRow,
						children: [(0, react_jsx_runtime.jsx)("span", {
							className: SubagentModelSelectionFields_module_css_default.toggleLabel,
							children: t("subagentModelSelectionToggle")
						}), (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Switch, {
							checked: state.enabled,
							label: t("subagentModelSelectionToggle"),
							disabled: !state.writable || state.saving,
							onChange: props.toggleEnabled
						})]
					}), (0, react_jsx_runtime.jsx)("p", {
						className: SubagentModelSelectionFields_module_css_default.hint,
						children: t(state.enabled ? "subagentModelSelectionChoose" : "subagentModelSelectionOff")
					})]
				}),
				state.enabled ? (0, react_jsx_runtime.jsxs)("div", {
					className: SubagentModelSelectionFields_module_css_default.selection,
					children: [
						state.catalogStatus === "loading" ? (0, react_jsx_runtime.jsx)("p", {
							className: SubagentModelSelectionFields_module_css_default.notice,
							role: "status",
							children: t("subagentModelSelectionLoading")
						}) : null,
						state.catalogStatus === "error" ? (0, react_jsx_runtime.jsxs)("div", {
							className: SubagentModelSelectionFields_module_css_default.catalogError,
							role: "alert",
							children: [(0, react_jsx_runtime.jsx)("span", { children: t("subagentModelSelectionLoadFailed") }), (0, react_jsx_runtime.jsx)("button", {
								type: "button",
								disabled: state.saving,
								onClick: props.retryCatalog,
								children: t("subagentModelSelectionRetry")
							})]
						}) : null,
						state.catalogPartial ? (0, react_jsx_runtime.jsx)("p", {
							className: SubagentModelSelectionFields_module_css_default.notice,
							children: t("subagentModelSelectionPartial")
						}) : null,
						state.candidates.length > 0 ? (0, react_jsx_runtime.jsxs)("fieldset", {
							className: SubagentModelSelectionFields_module_css_default.models,
							children: [
								(0, react_jsx_runtime.jsx)("legend", { children: t("subagentModelSelectionAllowed") }),
								[...availableGroups].map(([provider, group]) => (0, react_jsx_runtime.jsxs)("div", {
									className: SubagentModelSelectionFields_module_css_default.modelGroup,
									children: [(0, react_jsx_runtime.jsx)("div", {
										className: SubagentModelSelectionFields_module_css_default.providerName,
										children: group.providerName
									}), group.candidates.map(renderCandidate)]
								}, provider)),
								unavailable.length > 0 ? (0, react_jsx_runtime.jsxs)("div", {
									className: SubagentModelSelectionFields_module_css_default.modelGroup,
									children: [(0, react_jsx_runtime.jsx)("div", {
										className: SubagentModelSelectionFields_module_css_default.providerName,
										children: t("subagentModelSelectionUnavailableGroup")
									}), unavailable.map(renderCandidate)]
								}) : null
							]
						}) : state.catalogStatus === "ready" ? (0, react_jsx_runtime.jsx)("p", {
							className: SubagentModelSelectionFields_module_css_default.notice,
							children: t("subagentModelSelectionEmpty")
						}) : null,
						state.invalid ? (0, react_jsx_runtime.jsx)("p", {
							className: SubagentModelSelectionFields_module_css_default.invalid,
							children: t("subagentModelSelectionRequired")
						}) : null
					]
				}) : null,
				state.conflicted ? (0, react_jsx_runtime.jsx)("p", {
					className: SubagentModelSelectionFields_module_css_default.conflict,
					role: "status",
					children: t("subagentModelSelectionConflict")
				}) : null
			] });
		}
		//#endregion
		//#region lib/types/client/subagent-card-controller.js
		/** Shared presentation and actions for the two Host-owned Subagent settings sections. */
		/**
		* Derive the shared card state without duplicating either form's subscriptions.
		* @param limits - Current delegation-limit form.
		* @param models - Current model-authorization form.
		* @returns Availability and settlement across the sections this Host serves.
		*/
		function subagentCardShell(limits, models) {
			const sections = [limits, models].filter((section) => section.available);
			return {
				available: sections.length > 0,
				writable: sections.every((section) => section.writable),
				dirty: sections.some((section) => section.dirty),
				invalid: sections.some((section) => section.invalid) || models.available && models.dirty && models.conflicted,
				saving: sections.some((section) => section.saving),
				failed: sections.some((section) => section.failed)
			};
		}
		/**
		* Compose one card from the existing forms; each write retains its namespace revision fence.
		* @param limits - Limit form source and actions.
		* @param models - Model form source and actions.
		* @returns Framework-bound sources and shared save/discard actions.
		*/
		function subagentCardFace(limits, models) {
			return {
				hooks: {
					...limits.hooks,
					...models.hooks
				},
				editLimit: limits.edit,
				resetLimit: limits.resetField,
				toggleEnabled: models.toggleEnabled,
				toggleModel: models.toggleModel,
				retryCatalog: models.retryCatalog,
				save: () => {
					const limitState = limits.hooks.subagentLimitsCard.getSnapshot();
					const modelState = models.hooks.subagentModelSelectionCard.getSnapshot();
					const state = subagentCardShell(limitState, modelState);
					if (!state.available || !state.writable || !state.dirty || state.invalid || state.saving) return;
					if (modelState.available && modelState.dirty) models.save();
					if (limitState.available && limitState.dirty) limits.save();
				},
				discard: () => {
					if (subagentCardShell(limits.hooks.subagentLimitsCard.getSnapshot(), models.hooks.subagentModelSelectionCard.getSnapshot()).saving) return;
					limits.discard();
					models.discard();
				}
			};
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-settings-plugins/src/client/SubagentCard.module.css.mjs
		const css = ".rREoYG_section{min-width:0;padding:16px 0}.rREoYG_heading{color:var(--dsw-alias-label-primary);margin:0;font-size:13px;font-weight:600;line-height:1.5}";
		const tagId = "@deepseek-ai/dsh-client-ui-settings-plugins/SubagentCard.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-settings-plugins";
			tag.dataset.pluginCss = tagId;
			tag.textContent = css;
			document.head.appendChild(tag);
		}
		var SubagentCard_module_css_default = {
			"heading": "rREoYG_heading",
			"section": "rREoYG_section"
		};
		//#endregion
		//#region lib/types/client/SubagentCard.js
		/** One settings card for Subagent delegation limits and model authorization. */
		/**
		* Render the available Subagent settings with one configuration page and save footer.
		* @param props - Locale, both form snapshots, and their shared actions.
		* @returns The summary or the available settings form.
		*/
		function SubagentCard(props) {
			const { t } = props;
			const limits = props.useSubagentLimitsCard((snapshot) => snapshot);
			const models = props.useSubagentModelSelectionCard((snapshot) => snapshot);
			const headingId = (0, react.useId)();
			if (props.view === "summary") return t("subagentDescription");
			const state = subagentCardShell(limits, models);
			return (0, react_jsx_runtime.jsxs)(PluginConfigForm, {
				t,
				state,
				onSave: props.save,
				onDiscard: props.discard,
				children: [limits.available ? (0, react_jsx_runtime.jsxs)("section", {
					className: SubagentCard_module_css_default.section,
					"aria-labelledby": `${headingId}-limits`,
					children: [(0, react_jsx_runtime.jsx)("h3", {
						className: SubagentCard_module_css_default.heading,
						id: `${headingId}-limits`,
						children: t("subagentLimitsTitle")
					}), (0, react_jsx_runtime.jsx)(SubagentLimitsFields, {
						t,
						state: {
							...limits,
							saving: state.saving
						},
						edit: props.editLimit,
						resetField: props.resetLimit
					})]
				}) : null, models.available ? (0, react_jsx_runtime.jsxs)("section", {
					className: SubagentCard_module_css_default.section,
					"aria-labelledby": `${headingId}-models`,
					children: [(0, react_jsx_runtime.jsx)("h3", {
						className: SubagentCard_module_css_default.heading,
						id: `${headingId}-models`,
						children: t("subagentModelSelectionTitle")
					}), (0, react_jsx_runtime.jsx)(SubagentModelSelectionFields, {
						t,
						state: {
							...models,
							saving: state.saving
						},
						toggleEnabled: props.toggleEnabled,
						toggleModel: props.toggleModel,
						retryCatalog: props.retryCatalog
					})]
				}) : null]
			});
		}
		//#endregion
		//#region lib/types/client/card-form.js
		/**
		* Shared form model behind every plugin card.
		*
		* A card stages what the user types and writes it only when they save. Each
		* settings write is a durable, revision-fenced document mutation, so a control
		* that committed as it settled turned one edit into a write the user never
		* asked for and could not preview; staged text makes what is on screen exactly
		* what a save would store.
		*
		* A field shows its effective value — the user layer over the composition
		* layer over the schema default — and whether the user layer carries it. That
		* presence, not a value comparison, is what marks a field overridden: an
		* override equal to the composition default is still an override.
		*/
		/**
		* A whole-number field. An empty draft clears the field; any other draft that
		* is not a finite number blocks the save.
		* @param field - field name inside the namespace section.
		* @returns the field's conversion spec.
		*/
		function numberField(field) {
			return {
				field,
				format: (value) => typeof value === "number" ? String(value) : "",
				parse: (text) => {
					const trimmed = text.trim();
					if (trimmed === "") return { kind: "clear" };
					const parsed = Number(trimmed);
					return Number.isFinite(parsed) ? {
						kind: "set",
						value: parsed
					} : void 0;
				}
			};
		}
		/**
		* A free-text field. An empty draft clears the field, so emptying the control
		* and saving is the same gesture as resetting it.
		* @param field - field name inside the namespace section.
		* @returns the field's conversion spec.
		*/
		function textField(field) {
			return {
				field,
				format: (value) => typeof value === "string" ? value : "",
				parse: (text) => {
					const trimmed = text.trim();
					return trimmed === "" ? { kind: "clear" } : {
						kind: "set",
						value: trimmed
					};
				}
			};
		}
		/**
		* Stages one card's edits over one settings namespace and writes them on save.
		*
		* The form publishes through a snapshot store because slot components read
		* through a snapshot selector, while both the scope and the local drafts
		* change underneath; every projection is rebuilt from the two together.
		*/
		var CardForm = class {
			scope;
			specs;
			secretSpecs;
			staged = /* @__PURE__ */ new Map();
			listeners = /* @__PURE__ */ new Set();
			saving = false;
			failed = false;
			/**
			* @param scope - the bound settings scope for this card's namespace.
			* @param specs - the section fields this card edits.
			* @param secrets - the card's write-only controls, written outside the section.
			*/
			constructor(scope, specs, secrets = []) {
				this.scope = scope;
				this.specs = new Map(specs.map((spec) => [spec.field, spec]));
				this.secretSpecs = new Map(secrets.map((spec) => [spec.field, spec]));
				scope.subscribe(() => {
					this.publish();
				});
			}
			/**
			* Publish a projection of this form, rebuilt whenever the scope or a draft changes.
			* @param project - build the card's state from the form's current reads.
			* @returns the store the card's component reads through its bound selector.
			*/
			bind(project) {
				const store = (0, _deepseek_ai_dsh_client_store.createSnapshotStore)(project());
				this.listeners.add(() => {
					store.set(project());
				});
				return store;
			}
			/**
			* Read the card-level state: what the Host serves, and what a save would do.
			* @returns the form state every card shares.
			*/
			shell() {
				const snapshot = this.scope.getSnapshot();
				const plan = this.plan();
				return {
					available: snapshot.status === "ready",
					writable: snapshot.writable,
					dirty: plan.length > 0,
					invalid: plan.some((item) => item.run === void 0),
					saving: this.saving,
					failed: this.failed
				};
			}
			/**
			* Read one control's state.
			* @param field - field name of a section field or of a write-only control.
			* @returns the draft text, whether a save would leave an override, and whether it is invalid.
			*/
			field(field) {
				const staged = this.staged.get(field);
				if (this.secretSpecs.has(field)) return {
					text: staged?.text ?? "",
					overridden: false,
					invalid: false
				};
				const spec = this.spec(field);
				if (staged === void 0) return {
					text: spec.format(this.sectionValue(field)),
					overridden: this.stored(field),
					invalid: false
				};
				const write = staged.clear ? { kind: "clear" } : spec.parse(staged.text);
				return {
					text: staged.text,
					overridden: write?.kind === "set",
					invalid: write === void 0
				};
			}
			/**
			* Build the edit, reset, save, and discard actions bound to this form.
			* @returns the actions a card's slot entry injects.
			*/
			actions() {
				return {
					edit: (field, text) => {
						this.stage(field, {
							text,
							clear: false
						});
					},
					resetField: (field) => {
						this.stage(field, {
							text: this.spec(field).format(this.baseValue(field)),
							clear: true
						});
					},
					save: () => {
						this.save();
					},
					discard: () => {
						if (this.staged.size === 0 && !this.failed) return;
						this.staged.clear();
						this.failed = false;
						this.publish();
					}
				};
			}
			/**
			* Write every staged edit, then re-seed from what the Host accepted.
			*
			* The Host is the only authority on whether a value was accepted — its
			* validators own the constraints no schema can express — so the outcome is
			* read back from the section rather than predicted here. A save that did not
			* land keeps its drafts, so the user can correct them instead of retyping.
			* @returns settlement after every write and the read-back.
			*/
			async save() {
				const plan = this.plan();
				const writes = plan.flatMap((item) => item.run === void 0 ? [] : [item.run]);
				if (plan.length === 0 || this.saving || writes.length !== plan.length) return;
				this.saving = true;
				this.failed = false;
				this.publish();
				let landed = true;
				for (const write of writes) landed = await write() && landed;
				if (landed) this.staged.clear();
				this.saving = false;
				this.failed = !landed;
				this.publish();
			}
			/**
			* Every staged edit a save would write. An entry whose draft is not a value
			* its field accepts carries no write: the form is still dirty, and the save
			* refuses rather than dropping the edit.
			* @returns the planned writes, in the order the fields were staged.
			*/
			plan() {
				const plan = [];
				for (const [field, staged] of this.staged) {
					const secret = this.secretSpecs.get(field);
					if (secret !== void 0) {
						const value = staged.text.trim();
						if (value !== "") plan.push({
							field,
							run: () => secret.write(value)
						});
						continue;
					}
					const spec = this.spec(field);
					if (staged.clear) {
						if (this.stored(field)) plan.push({
							field,
							run: () => this.clear(field)
						});
						continue;
					}
					if (staged.text === spec.format(this.sectionValue(field))) continue;
					const write = spec.parse(staged.text);
					if (write === void 0) plan.push({
						field,
						run: void 0
					});
					else if (write.kind === "clear") plan.push({
						field,
						run: () => this.clear(field)
					});
					else plan.push({
						field,
						run: () => this.store(field, write.value)
					});
				}
				return plan;
			}
			async clear(field) {
				await this.scope.unset(field);
				return !this.stored(field);
			}
			async store(field, value) {
				await this.scope.set(field, value);
				return this.userLayer()?.[field] === value;
			}
			stage(field, edit) {
				this.staged.set(field, edit);
				this.failed = false;
				this.publish();
			}
			spec(field) {
				const spec = this.specs.get(field);
				if (spec === void 0) throw new Error(`plugin card has no field ${field}`);
				return spec;
			}
			snapshotOf() {
				return this.scope.getSnapshot();
			}
			sectionValue(field) {
				return this.snapshotOf().value?.[field];
			}
			baseValue(field) {
				return this.snapshotOf().base?.[field];
			}
			userLayer() {
				return this.snapshotOf().user;
			}
			stored(field) {
				const user = this.userLayer();
				return user !== void 0 && Object.hasOwn(user, field);
			}
			publish() {
				for (const listener of this.listeners) listener();
			}
		};
		//#endregion
		//#region lib/types/client/subagent-limits-card-controller.js
		/** Staged delegation limits backed by the Host's subagent settings section. */
		function limitField(field, minimum) {
			const numeric = numberField(field);
			return {
				...numeric,
				parse: (text) => {
					const write = numeric.parse(text);
					if (write?.kind !== "set") return write;
					const value = write.value;
					return Number.isSafeInteger(value) && value >= minimum && !Object.is(value, -0) ? write : void 0;
				}
			};
		}
		/** Bind two independently resettable limits to one staged settings form. */
		var SubagentLimitsCardController = class {
			form;
			store;
			/** @param scope - The Host's `subagent` settings section. */
			constructor(scope) {
				this.form = new CardForm(scope, [limitField("maxDepth", 0), limitField("maxActiveSubagents", 1)]);
				this.store = this.form.bind(() => ({
					...this.form.shell(),
					maxDepth: this.form.field("maxDepth"),
					maxActiveSubagents: this.form.field("maxActiveSubagents")
				}));
			}
			/**
			* Bind the limits editor to the slot renderer.
			* @returns The limits snapshot and staged write actions.
			*/
			inject() {
				return {
					hooks: { subagentLimitsCard: this.store },
					...this.form.actions()
				};
			}
		};
		//#endregion
		//#region lib/types/client/WebSearchCard.js
		/**
		* Render the web-search provider's one-liner or its configuration form, as the Plugins page asks.
		* @param props - the view asked for, locale copy, the form snapshot, and its actions.
		* @returns the one-liner, or the form.
		*/
		function WebSearchCard(props) {
			const { t } = props;
			const state = props.useWebSearchCard((snapshot) => snapshot);
			if (props.view === "summary") return t("webSearchDescription");
			const disabled = !state.writable;
			return (0, react_jsx_runtime.jsxs)(PluginConfigForm, {
				t,
				state,
				onSave: props.save,
				onDiscard: props.discard,
				children: [
					(0, react_jsx_runtime.jsx)(SecretField, {
						id: "plugin-config-web-search-key",
						label: t("webSearchApiKey"),
						hint: t("webSearchApiKeyHint"),
						disabled: !state.apiKeyWritable,
						text: state.apiKey.text,
						configured: state.apiKeyConfigured,
						stateLabel: state.apiKeyConfigured ? t("webSearchApiKeySet") : t("webSearchApiKeyUnset"),
						onEdit: (text) => {
							props.edit("apiKey", text);
						}
					}),
					(0, react_jsx_runtime.jsx)(ValueField, {
						id: "plugin-config-web-search-endpoint",
						label: t("webSearchBaseUrl"),
						hint: t("webSearchBaseUrlHint"),
						overriddenLabel: t("overridden"),
						resetLabel: t("reset"),
						invalidLabel: t("invalidNumber"),
						disabled,
						...state.baseURL,
						onEdit: (text) => {
							props.edit("baseURL", text);
						},
						onReset: () => {
							props.resetField("baseURL");
						}
					}),
					(0, react_jsx_runtime.jsx)(ValueField, {
						id: "plugin-config-web-search-max-uses",
						label: t("webSearchMaxUses"),
						hint: t("webSearchMaxUsesHint"),
						overriddenLabel: t("overridden"),
						resetLabel: t("reset"),
						invalidLabel: t("invalidNumber"),
						numeric: true,
						disabled,
						...state.maxUses,
						onEdit: (text) => {
							props.edit("maxUses", text);
						},
						onReset: () => {
							props.resetField("maxUses");
						}
					})
				]
			});
		}
		//#endregion
		//#region lib/types/client/agent-loop-card-controller.js
		/** The agent-loop card's staged form over the `agent-loop` settings namespace. */
		/**
		* Namespace of the agent loop's user-owned settings. Spelled here rather than
		* imported: a client package must not depend on a Host package.
		*/
		const AGENT_LOOP_NS = "agent-loop";
		/** Bridges the `agent-loop` scope onto the card's staged form. */
		var AgentLoopCardController = class {
			form;
			store;
			/** @param scope - the bound settings scope for the `agent-loop` namespace. */
			constructor(scope) {
				this.form = new CardForm(scope, [numberField("maxParallelToolCalls")]);
				this.store = this.form.bind(() => this.projection());
			}
			projection() {
				return {
					...this.form.shell(),
					maxParallelToolCalls: this.form.field("maxParallelToolCalls")
				};
			}
			/**
			* Build the face the card's slot registration injects.
			* @returns the card's snapshot and its form actions.
			*/
			inject() {
				return {
					hooks: { agentLoopCard: this.store },
					...this.form.actions()
				};
			}
		};
		//#endregion
		//#region lib/types/client/bash-card-controller.js
		/** The shell card's staged form over the `bash` settings namespace. */
		/**
		* Namespace of the shell capability. Spelled here rather than imported: a
		* client package must not depend on a Host package, and the executor families
		* that own it spell the same value.
		*/
		const SHELL_NS = "shell";
		/** Bridges the `bash` scope onto the shell card's staged form. */
		var BashCardController = class {
			form;
			store;
			/** @param scope - the bound settings scope for the `bash` namespace. */
			constructor(scope) {
				this.form = new CardForm(scope, [numberField("timeoutMs"), numberField("maxOutputBytes")]);
				this.store = this.form.bind(() => this.projection());
			}
			projection() {
				return {
					...this.form.shell(),
					timeoutMs: this.form.field("timeoutMs"),
					maxOutputBytes: this.form.field("maxOutputBytes")
				};
			}
			/**
			* Build the face the card's slot registration injects.
			* @returns the card's snapshot and its form actions.
			*/
			inject() {
				return {
					hooks: { bashCard: this.store },
					...this.form.actions()
				};
			}
		};
		//#endregion
		//#region lib/types/client/subagent-model-selection-card-controller.js
		/** Staged editor for the Host-owned subagent model allowlist. */
		/** Namespace of the Host-owned subagent model-selection preference. */
		const SUBAGENT_MODEL_SELECTION_NS = "subagent-model-selection";
		/**
		* Stable identity for one exact route; callers resolve it by lookup and never parse it.
		* @param route - Provider/model route to identify.
		* @returns Opaque key for lookup within the card.
		*/
		function subagentModelKey(route) {
			return `${route.provider}\0${route.model}`;
		}
		/**
		* Join live adapter metadata with stored routes that remain removable after disappearance.
		* @param groups - Current model directory grouped by provider.
		* @param stored - Routes in the effective settings value.
		* @param selected - Opaque route keys selected in the current draft.
		* @returns Candidate rows for the card.
		*/
		function subagentModelCandidates(groups, stored, selected) {
			const storedByKey = new Map(stored.map((route) => [subagentModelKey(route), route]));
			const candidates = groups.flatMap((group) => group.models.map((model) => {
				const route = {
					provider: group.id,
					model: model.id
				};
				const key = subagentModelKey(route);
				storedByKey.delete(key);
				return {
					...route,
					key,
					providerName: group.name,
					modelName: model.name,
					available: true,
					selected: selected.has(key)
				};
			}));
			for (const route of storedByKey.values()) {
				const key = subagentModelKey(route);
				candidates.push({
					...route,
					key,
					providerName: route.provider,
					modelName: route.model,
					available: false,
					selected: selected.has(key)
				});
			}
			return candidates;
		}
		function sameRoutes(left, right) {
			if (left.length !== right.length) return false;
			const rightKeys = new Set(right.map(subagentModelKey));
			return left.every((route) => rightKeys.has(subagentModelKey(route)));
		}
		/** Bridges one settings scope and the live adapter directory onto a staged card. */
		var SubagentModelSelectionCardController = class {
			scope;
			ctx;
			catalogGroups = [];
			catalogPartial = false;
			catalogStatus = "idle";
			draftEnabled;
			draftRoutes;
			draftRevision;
			saving = false;
			failed = false;
			conflicted = false;
			disposed = false;
			saveGeneration = 0;
			catalogGeneration = 0;
			store;
			unsubscribe;
			/**
			* @param scope - bound `subagent-model-selection` settings scope.
			* @param ctx - the card plugin's context, whose `remote.session` namespace
			* answers the Host model catalog.
			*/
			constructor(scope, ctx) {
				this.scope = scope;
				this.ctx = ctx;
				this.store = (0, _deepseek_ai_dsh_client_store.createSnapshotStore)(this.projection());
				this.unsubscribe = scope.subscribe(() => {
					if (!this.saving && this.draftRoutes !== void 0 && this.scope.getSnapshot().revision !== this.draftRevision) if (this.currentEnabled() === this.enabled() && sameRoutes(this.currentRoutes(), this.desiredRoutes())) this.clearDraft();
					else this.conflicted = true;
					if (this.enabled() && this.catalogStatus === "idle") this.loadCatalog();
					this.publish();
				});
				if (this.enabled() && this.catalogStatus === "idle") this.loadCatalog();
			}
			/** Stop observing settings and suppress late directory/write settlements. */
			dispose() {
				this.disposed = true;
				this.saveGeneration += 1;
				this.catalogGeneration += 1;
				this.unsubscribe();
			}
			/**
			* Build the renderer face for this card.
			* @returns The snapshot and staged card actions injected into the renderer.
			*/
			inject() {
				return {
					hooks: { subagentModelSelectionCard: this.store },
					toggleEnabled: () => {
						this.toggleEnabled();
					},
					toggleModel: (key) => {
						this.toggleModel(key);
					},
					retryCatalog: () => {
						this.loadCatalog();
					},
					save: () => {
						this.save();
					},
					discard: () => {
						this.discard();
					}
				};
			}
			currentRoutes() {
				return this.scope.getSnapshot().value?.allowedModels.map((route) => ({ ...route })) ?? [];
			}
			currentEnabled() {
				return this.scope.getSnapshot().value?.enabled ?? false;
			}
			selected() {
				return new Set(this.draftRoutes?.keys() ?? this.currentRoutes().map(subagentModelKey));
			}
			enabled() {
				return this.draftEnabled ?? this.currentEnabled();
			}
			beginDraft() {
				if (this.draftRoutes === void 0) {
					const snapshot = this.scope.getSnapshot();
					this.draftEnabled = snapshot.value?.enabled ?? false;
					this.draftRoutes = new Map(snapshot.value?.allowedModels.map((route) => [subagentModelKey(route), { ...route }]) ?? []);
					this.draftRevision = snapshot.revision;
				}
				return this.draftRoutes;
			}
			toggleEnabled() {
				const snapshot = this.scope.getSnapshot();
				if (this.disposed || snapshot.status !== "ready" || !snapshot.writable || this.saving) return;
				this.beginDraft();
				this.draftEnabled = !this.draftEnabled;
				this.failed = false;
				if (this.draftEnabled && this.catalogStatus === "idle") this.loadCatalog();
				this.publish();
			}
			toggleModel(key) {
				if (!this.enabled() || this.saving || !this.scope.getSnapshot().writable) return;
				const candidate = this.candidates().find((candidate) => candidate.key === key);
				if (candidate === void 0) return;
				const routes = this.beginDraft();
				if (routes.has(key)) routes.delete(key);
				else routes.set(key, {
					provider: candidate.provider,
					model: candidate.model
				});
				this.failed = false;
				this.publish();
			}
			clearDraft() {
				this.draftEnabled = void 0;
				this.draftRoutes = void 0;
				this.draftRevision = void 0;
				this.failed = false;
				this.conflicted = false;
			}
			discard() {
				if (this.saving) return;
				this.clearDraft();
				this.publish();
			}
			candidates() {
				const retained = new Map(this.currentRoutes().map((route) => [subagentModelKey(route), route]));
				for (const [key, route] of this.draftRoutes ?? []) retained.set(key, route);
				return subagentModelCandidates(this.catalogGroups, [...retained.values()], this.selected());
			}
			desiredRoutes() {
				return [...this.draftRoutes?.values() ?? this.currentRoutes()].map((route) => ({ ...route }));
			}
			async save() {
				const snapshot = this.scope.getSnapshot();
				const desiredEnabled = this.enabled();
				const desired = this.desiredRoutes();
				if (this.disposed || snapshot.status !== "ready" || !snapshot.writable || this.saving || this.currentEnabled() === desiredEnabled && sameRoutes(this.currentRoutes(), desired) || desiredEnabled && desired.length === 0) return;
				if (this.draftRoutes !== void 0 && snapshot.revision !== this.draftRevision) {
					this.conflicted = true;
					this.publish();
					return;
				}
				const generation = this.saveGeneration;
				this.saving = true;
				this.failed = false;
				this.conflicted = false;
				this.publish();
				await this.scope.mutate([{
					op: "set",
					path: ["enabled"],
					value: desiredEnabled
				}, {
					op: "set",
					path: ["allowedModels"],
					value: desired.map((route) => ({
						provider: route.provider,
						model: route.model
					}))
				}], this.draftRevision);
				if (generation !== this.saveGeneration) return;
				const landed = this.currentEnabled() === desiredEnabled && sameRoutes(this.currentRoutes(), desired);
				this.saving = false;
				this.failed = !landed;
				if (landed) this.clearDraft();
				this.publish();
			}
			/** Invalidate and reload model candidates after a Host model input changes. */
			refreshCatalog() {
				if (this.disposed) return;
				this.catalogGeneration += 1;
				this.catalogStatus = "idle";
				this.catalogPartial = false;
				if (this.enabled()) this.loadCatalog();
				else this.publish();
			}
			/** Drop Host-specific candidates and drafts, then reload after reconnecting. */
			resetConnection() {
				if (this.disposed) return;
				this.saveGeneration += 1;
				this.saving = false;
				this.clearDraft();
				this.catalogGroups = [];
				this.refreshCatalog();
			}
			async loadCatalog() {
				if (this.disposed || this.catalogStatus === "loading") return;
				const generation = this.catalogGeneration;
				this.catalogStatus = "loading";
				this.catalogPartial = false;
				this.publish();
				const response = await this.ctx.remote.session.modelCatalog();
				if (generation !== this.catalogGeneration) return;
				if (response.ok) {
					this.catalogGroups = response.value.groups;
					this.catalogPartial = response.value.failures.length > 0;
					this.catalogStatus = "ready";
				} else this.catalogStatus = "error";
				this.publish();
			}
			projection() {
				const snapshot = this.scope.getSnapshot();
				const current = this.currentRoutes();
				const desired = this.desiredRoutes();
				const enabled = this.enabled();
				return {
					available: snapshot.status === "ready",
					writable: snapshot.writable,
					dirty: this.currentEnabled() !== enabled || !sameRoutes(current, desired),
					invalid: enabled && desired.length === 0,
					saving: this.saving,
					failed: this.failed,
					enabled,
					candidates: this.candidates(),
					catalogStatus: this.catalogStatus,
					catalogPartial: this.catalogPartial,
					conflicted: this.conflicted
				};
			}
			publish() {
				this.store.set(this.projection());
			}
		};
		//#endregion
		//#region lib/types/client/web-search-card-controller.js
		/**
		* The web-search card's staged form over the `web-search-deepseek` settings
		* namespace.
		*
		* The key is the one control that does not live in the section: its literal
		* never rides a response, so the card learns only whether one is configured
		* and writes it through the credentials domain, addressed by the reference the
		* section names. It is still staged with the rest of the form, so one save
		* covers everything the card shows.
		*/
		/**
		* Namespace of the DeepSeek search provider. Spelled here rather than
		* imported: a client package must not depend on a Host package.
		*/
		const WEB_SEARCH_NS = "web-search-deepseek";
		/** Credential reference the provider resolves when the section names none. */
		const DEFAULT_API_KEY_REF = "DEEPSEEK_API_KEY";
		/** Form field the credential control stages under. */
		const API_KEY_FIELD = "apiKey";
		/** Bridges the `web-search-deepseek` scope and the credentials domain onto the card. */
		var WebSearchCardController = class {
			scope;
			ctx;
			form;
			store;
			credential = {
				ref: "",
				configured: false,
				writable: true
			};
			/**
			* @param scope - the bound settings scope for the `web-search-deepseek` namespace.
			* @param ctx - the card plugin's context, whose `remote.credentials` namespace
			* answers for the credential the section references.
			*/
			constructor(scope, ctx) {
				this.scope = scope;
				this.ctx = ctx;
				this.form = new CardForm(scope, [textField("baseURL"), numberField("maxUses")], [{
					field: API_KEY_FIELD,
					write: (text) => this.writeKey(text)
				}]);
				this.store = this.form.bind(() => this.projection());
				scope.subscribe(() => {
					this.readCredential();
				});
				this.readCredential();
			}
			projection() {
				return {
					...this.form.shell(),
					baseURL: this.form.field("baseURL"),
					maxUses: this.form.field("maxUses"),
					apiKey: this.form.field(API_KEY_FIELD),
					apiKeyConfigured: this.credential.configured,
					apiKeyWritable: this.credential.writable
				};
			}
			/**
			* Ask the credentials domain about the reference the section currently names.
			*
			* The answer is stored with the reference it describes: `apiKeyEnv` can
			* change between the request and its response, and two reads can settle out
			* of order, so a response is published only while it still answers for the
			* reference in force.
			*/
			async readCredential() {
				const ref = refOf(this.scope.getSnapshot());
				if (ref !== this.credential.ref) {
					this.credential = {
						ref,
						configured: false,
						writable: true
					};
					this.store.set(this.projection());
				}
				const response = await this.ctx.remote.credentials.describe([ref]);
				if (!response.ok || ref !== refOf(this.scope.getSnapshot())) return;
				const view = response.value[ref];
				const next = {
					ref,
					configured: view?.configured ?? false,
					writable: view?.writable ?? true
				};
				if (next.configured === this.credential.configured && next.writable === this.credential.writable) return;
				this.credential = next;
				this.store.set(this.projection());
			}
			/**
			* Re-read after the Host reports a change to the reference this card watches.
			*
			* A key can be written from somewhere else — the Models page addresses the
			* same reference — and the settings section does not change when it is, so
			* without this the badge keeps reporting a state the Host already replaced.
			* @param ref - the reference the Host reports as changed.
			*/
			refreshCredential(ref) {
				if (ref !== this.credential.ref) return;
				this.readCredential();
			}
			/**
			* Build the face the card's slot registration injects.
			* @returns the card's snapshot and its form actions.
			*/
			inject() {
				return {
					hooks: { webSearchCard: this.store },
					...this.form.actions()
				};
			}
			/**
			* Write the staged key, then re-read whether the Host now holds one.
			* @param value - the staged credential literal.
			* @returns whether the Host reports a configured credential afterwards.
			*/
			async writeKey(value) {
				await this.ctx.remote.credentials.set(refOf(this.scope.getSnapshot()), value);
				await this.readCredential();
				return this.credential.configured;
			}
		};
		/**
		* The credential reference the section names, or the provider's default.
		* @param snapshot - the current scope snapshot.
		* @returns the reference to address.
		*/
		function refOf(snapshot) {
			const declared = snapshot.value?.apiKeyEnv;
			return declared !== void 0 && declared.length > 0 ? declared : DEFAULT_API_KEY_REF;
		}
		//#endregion
		//#region lib/types/client/locales.js
		/** Locale bundles for the built-in plugins settings section and the plugin configuration pages. */
		/** English copy. */
		const en = {
			nav: "Built-in plugins",
			title: "Built-in plugins",
			intro: "Inspect the plugins this deployment ships.",
			tabs: "Plugin views",
			empty: "This deployment exposes no plugin views.",
			overridden: "Overridden",
			reset: "Reset to default",
			readOnly: "This deployment stores settings read-only.",
			unavailable: "This plugin is not loaded, so it cannot be configured right now.",
			save: "Save",
			saving: "Saving…",
			saveFailed: "The deployment did not accept these values; they were left for you to correct.",
			invalidNumber: "Enter a number, or leave blank to use the default.",
			bashTitle: "Shell",
			bashDescription: "Limits every command the agent runs.",
			bashTimeoutMs: "Command timeout (ms)",
			bashTimeoutMsHint: "How long one command may run before it is terminated.",
			bashMaxOutputBytes: "Output cap per stream (bytes)",
			bashMaxOutputBytesHint: "Output beyond this spills to a temporary file rather than being lost.",
			agentLoopTitle: "Agent loop",
			agentLoopDescription: "How the agent dispatches tool calls.",
			agentLoopMaxParallel: "Parallel tool calls",
			agentLoopMaxParallelHint: "Upper bound on parallel-safe calls running at once within one step.",
			webSearchTitle: "Web search",
			webSearchDescription: "The DeepSeek search provider.",
			webSearchApiKey: "API key",
			webSearchApiKeyHint: "Stored outside the settings file. Leave blank to keep the current key.",
			webSearchApiKeySet: "A key is configured.",
			webSearchApiKeyUnset: "No key is configured; search is unavailable until one is.",
			webSearchBaseUrl: "Endpoint",
			webSearchBaseUrlHint: "Leave blank to use the provider default.",
			webSearchMaxUses: "Max searches per request",
			webSearchMaxUsesHint: "How many times one request may search before it must answer.",
			subagentTitle: "Subagent",
			subagentDescription: "Set Subagent recursion depth, count, and models.",
			subagentLimitsTitle: "Limits",
			subagentMaxDepth: "Maximum recursion depth",
			subagentDepthHelpLabel: "About maximum recursion depth",
			subagentDepthHelp: "Limits how many levels of Subagents an Agent can create.",
			subagentDepthZero: "Disable Subagents",
			subagentDepthOne: "Only the main Agent can create Subagents",
			subagentDepthOverride: "If a tool defines its own maximum recursion depth, that setting takes precedence.",
			subagentMaxActive: "Subagent parallelism limit",
			subagentCapacityHelpLabel: "About the Subagent parallelism limit",
			subagentCapacityHelp: "Total live Subagents under the same main Agent, across all recursion levels. The main Agent is excluded. New start requests are rejected when the limit is reached.",
			subagentDepthInvalid: "Enter a whole number of 0 or more.",
			subagentCapacityInvalid: "Enter a whole number of 1 or more.",
			subagentModelSelectionTitle: "Model selection",
			subagentModelSelectionToggle: "Allow agents to choose models for Subagents",
			subagentModelSelectionChoose: "When enabled, agents can choose a provider, model, and reasoning effort for each Subagent from the authorized models below. Applies only to new sessions.",
			subagentModelSelectionAllowed: "Models agents may choose",
			subagentModelSelectionLoading: "Loading models…",
			subagentModelSelectionLoadFailed: "Models could not be loaded.",
			subagentModelSelectionRetry: "Retry",
			subagentModelSelectionPartial: "Some model providers could not be loaded; saved choices remain removable.",
			subagentModelSelectionUnavailable: "Currently unavailable",
			subagentModelSelectionUnavailableGroup: "Saved but currently unavailable",
			subagentModelSelectionEmpty: "No model provider currently advertises a model.",
			subagentModelSelectionRequired: "Select at least one model before saving.",
			subagentModelSelectionConflict: "Settings changed elsewhere. Discard your draft and try again.",
			subagentModelSelectionOff: "Subagents use configured defaults or inherit the parent agent's model. Saved model choices are retained."
		};
		/** Simplified Chinese copy. */
		const zh = {
			nav: "内置插件",
			title: "内置插件",
			intro: "查看内置部署的插件列表",
			tabs: "插件视图",
			empty: "本部署没有开放任何插件视图。",
			overridden: "已覆盖",
			reset: "恢复默认",
			readOnly: "本部署的设置为只读。",
			unavailable: "该插件当前未加载，暂时无法配置。",
			save: "保存",
			saving: "保存中…",
			saveFailed: "本部署没有接受这些值，已保留供你修改。",
			invalidNumber: "请填数字；留空表示使用默认值。",
			bashTitle: "终端",
			bashDescription: "限制 agent 运行的每一条命令。",
			bashTimeoutMs: "命令超时（毫秒）",
			bashTimeoutMsHint: "单条命令允许运行多久，超时即终止。",
			bashMaxOutputBytes: "单流输出上限（字节）",
			bashMaxOutputBytesHint: "超出部分会转存到临时文件，而不是被丢弃。",
			agentLoopTitle: "Agent 循环",
			agentLoopDescription: "Agent 如何派发工具调用。",
			agentLoopMaxParallel: "并行工具调用数",
			agentLoopMaxParallelHint: "同一步内最多同时运行多少个可并行的调用。",
			webSearchTitle: "网页搜索",
			webSearchDescription: "DeepSeek 搜索提供方。",
			webSearchApiKey: "API Key",
			webSearchApiKeyHint: "不写入设置文件。留空表示保持当前密钥。",
			webSearchApiKeySet: "已配置密钥。",
			webSearchApiKeyUnset: "未配置密钥；配置之前搜索不可用。",
			webSearchBaseUrl: "接口地址",
			webSearchBaseUrlHint: "留空则使用提供方默认地址。",
			webSearchMaxUses: "单次请求最多搜索次数",
			webSearchMaxUsesHint: "一次请求在必须作答前最多可以搜索多少次。",
			subagentTitle: "Subagent",
			subagentDescription: "设置 Subagent 的递归层级、数量和模型。",
			subagentLimitsTitle: "运行限制",
			subagentMaxDepth: "最大递归深度",
			subagentDepthHelpLabel: "最大递归深度说明",
			subagentDepthHelp: "限制 Agent 创建 Subagent 的递归层级。",
			subagentDepthZero: "禁用 Subagent",
			subagentDepthOne: "仅允许主 Agent 创建 Subagent",
			subagentDepthOverride: "如果某个工具单独设置了最大递归深度，以该工具的设置为准。",
			subagentMaxActive: "Subagent 并行数量上限",
			subagentCapacityHelpLabel: "Subagent 并行数量上限说明",
			subagentCapacityHelp: "同一主 Agent 下，所有递归层级同时存活的 Subagent 总数，主 Agent 不计入。达到上限时，新的启动请求会被拒绝。",
			subagentDepthInvalid: "请输入不小于 0 的整数。",
			subagentCapacityInvalid: "请输入不小于 1 的整数。",
			subagentModelSelectionTitle: "模型选择",
			subagentModelSelectionToggle: "允许 Agent 为 Subagent 选择模型",
			subagentModelSelectionChoose: "开启后，Agent 可以从下方授权模型中，为每个 Subagent 选择提供方、模型和推理强度。仅影响新会话。",
			subagentModelSelectionAllowed: "Agent 可选择的模型",
			subagentModelSelectionLoading: "正在加载模型…",
			subagentModelSelectionLoadFailed: "无法加载模型。",
			subagentModelSelectionRetry: "重试",
			subagentModelSelectionPartial: "部分模型提供方暂时无法加载；已保存的选择仍可移除。",
			subagentModelSelectionUnavailable: "当前不可用",
			subagentModelSelectionUnavailableGroup: "已保存但当前不可用",
			subagentModelSelectionEmpty: "当前没有模型提供方公布模型。",
			subagentModelSelectionRequired: "保存前请至少选择一个模型。",
			subagentModelSelectionConflict: "设置已在其他位置更新。请放弃修改后重试。",
			subagentModelSelectionOff: "关闭后，Subagent 使用配置的默认模型或继承父 Agent 的模型；已选模型会保留。"
		};
		//#endregion
		//#region lib/types/client/index.js
		/**
		* Built-in plugins settings surface and the official plugin configuration
		* pages, browser half. The Settings section is the shell around the
		* feature-owned tabs registered into `settings.plugins.tab` (the read-only
		* inventory ships one); the configuration pages this package ships register
		* into the Plugins page's `plugins.item` slot, for the host-plane namespaces
		* the deployment exposes, and appear in the page's Official group. Each form
		* binds its namespace through the client settings scope, which keeps the
		* pages unaware of one another and of the section.
		*/
		/** Dictionary namespace owned by this plugin. */
		const NS = "settings.plugins";
		/** Required services (cordis fiber inject). */
		const inject = [
			"slots",
			"locale",
			"remote",
			"remote.credentials",
			"remote.session",
			"settingsScope"
		];
		/**
		* Mount the built-in plugins section and the configuration pages this package ships.
		* @param ctx - the browser plugin context.
		*/
		function apply(ctx) {
			const t = ctx.locale.bind(NS);
			ctx.effect(() => ctx.locale.register(NS, {
				zh,
				en
			}), "ui-settings-plugins: section dictionaries");
			const bash = new BashCardController(ctx.settingsScope.bind({ namespace: SHELL_NS }));
			const agentLoop = new AgentLoopCardController(ctx.settingsScope.bind({ namespace: AGENT_LOOP_NS }));
			const webSearch = new WebSearchCardController(ctx.settingsScope.bind({ namespace: WEB_SEARCH_NS }), ctx);
			const subagentLimits = new SubagentLimitsCardController(ctx.settingsScope.bind({ namespace: "subagent" }));
			const subagentModelSelection = new SubagentModelSelectionCardController(ctx.settingsScope.bind({ namespace: SUBAGENT_MODEL_SELECTION_NS }), ctx);
			const subagentLimitsFace = subagentLimits.inject();
			const subagentModelsFace = subagentModelSelection.inject();
			ctx.effect(() => ctx.remote.$on("credentials/reference-updated", (ref) => {
				webSearch.refreshCredential(ref);
			}), "ui-settings-plugins: credential invalidations");
			ctx.effect(() => ctx.remote.$on("llm/adapters-updated", () => {
				subagentModelSelection.refreshCatalog();
			}), "ui-settings-plugins: subagent adapter invalidations");
			ctx.effect(() => ctx.remote.$on("settings/document-updated", () => {
				subagentModelSelection.refreshCatalog();
			}), "ui-settings-plugins: subagent settings invalidations");
			ctx.effect(() => ctx.on("connection/reset", () => {
				subagentModelSelection.resetConnection();
			}), "ui-settings-plugins: subagent connection generation");
			ctx.effect(() => () => {
				subagentModelSelection.dispose();
			}, "ui-settings-plugins: subagent preference");
			const pages = [
				[[SHELL_NS], () => ctx.slots.inject("plugins.item", () => ctx.slots.register({
					name: "plugins.item",
					id: "bash",
					order: 10,
					label: () => t("bashTitle"),
					locale: NS,
					inject: () => bash.inject()
				}, BashCard))],
				[[AGENT_LOOP_NS], () => ctx.slots.inject("plugins.item", () => ctx.slots.register({
					name: "plugins.item",
					id: "agent-loop",
					order: 20,
					label: () => t("agentLoopTitle"),
					locale: NS,
					inject: () => agentLoop.inject()
				}, AgentLoopCard))],
				[["subagent", SUBAGENT_MODEL_SELECTION_NS], () => ctx.slots.inject("plugins.item", () => ctx.slots.register({
					name: "plugins.item",
					id: "subagent",
					order: 30,
					label: () => t("subagentTitle"),
					locale: NS,
					inject: () => subagentCardFace(subagentLimitsFace, subagentModelsFace)
				}, SubagentCard))],
				[[WEB_SEARCH_NS], () => ctx.slots.inject("plugins.item", () => ctx.slots.register({
					name: "plugins.item",
					id: "web-search",
					order: 40,
					label: () => t("webSearchTitle"),
					locale: NS,
					inject: () => webSearch.inject()
				}, WebSearchCard))]
			];
			const describeFace = ctx.settingsScope.describe();
			ctx.effect(() => {
				const registered = /* @__PURE__ */ new Map();
				const sync = () => {
					const served = new Set(describeFace.getSnapshot().view?.namespaces.map((view) => view.ns) ?? []);
					for (const [namespaces, register] of pages) {
						const namespace = namespaces[0];
						const available = namespaces.some((namespace) => served.has(namespace));
						const off = registered.get(namespace);
						if (available && off === void 0) registered.set(namespace, register());
						else if (!available && off !== void 0) {
							off();
							registered.delete(namespace);
						}
					}
				};
				const unsubscribe = describeFace.subscribe(sync);
				describeFace.ensure();
				sync();
				return () => {
					unsubscribe();
					for (const off of registered.values()) off();
					registered.clear();
				};
			}, "ui-settings-plugins: configuration pages");
			let tabsVersion = -1;
			let tabsRevision = -1;
			let tabs = [];
			const sectionInjected = () => ({ hooks: { tabs: {
				getSnapshot: () => {
					const version = ctx.slots.getVersion("settings.plugins.tab");
					const revision = ctx.locale.getSnapshot().revision;
					if (version !== tabsVersion || revision !== tabsRevision) {
						tabsVersion = version;
						tabsRevision = revision;
						tabs = ctx.slots.entries("settings.plugins.tab").map((entry) => ({
							/* v8 ignore next -- list-slot registration requires id */
							id: entry.options.id ?? "",
							order: entry.options.order ?? 0,
							label: (0, _deepseek_ai_dsh_client_ui_slots.resolveSlotLabel)(entry.options.label) ?? ""
						})).sort((a, b) => a.order - b.order);
					}
					return tabs;
				},
				subscribe: (listener) => {
					const offLedger = ctx.slots.subscribe("settings.plugins.tab", listener);
					const offLocale = ctx.locale.subscribe(listener);
					return () => {
						offLedger();
						offLocale();
					};
				}
			} } });
			ctx.slots.inject("settings.section", () => ctx.slots.register({
				name: "settings.section",
				id: "plugins",
				order: 15,
				label: () => t("nav"),
				locale: NS,
				inject: sectionInjected,
				children: { "settings.plugins.tab": {
					kind: "list",
					scope: "root"
				} }
			}, PluginsSettingsSection));
		}
		//#endregion
		exports.apply = apply;
		exports.inject = inject;
		return module.exports;
	}
});

//# sourceMappingURL=client.js.map