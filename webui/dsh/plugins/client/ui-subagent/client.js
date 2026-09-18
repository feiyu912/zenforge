window.__ModuleLoader__.load({
	id: "@deepseek-ai/dsh-client-ui-subagent",
	factory: (require) => {
		var module = { exports: {} };
		var exports = module.exports;
		Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
		let react_jsx_runtime = require("react/jsx-runtime");
		let react = require("react");
		let react_dom = require("react-dom");
		let _deepseek_ai_dsh_client_ui_primitives = require("@deepseek-ai/dsh-client-ui-primitives");
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-subagent/src/client/SubagentHeaderLineage.module.css.mjs
		const css$2 = ".rhoEmW_root{align-items:center;gap:10px;min-width:0;display:inline-flex;position:relative}.rhoEmW_switcherRoot{min-width:0;margin-left:6px}.rhoEmW_trigger,.rhoEmW_switcherTrigger{min-height:28px;color:var(--dsw-alias-label-tertiary);cursor:pointer;background:0 0;border:0;border-radius:6px;align-items:center;padding:3px 2px;font-size:12px;line-height:18px;display:inline-flex}.rhoEmW_trigger{gap:4px}.rhoEmW_switcherTrigger{min-width:0;max-width:244px;color:var(--dsw-alias-label-primary);gap:4px;font-weight:500}.rhoEmW_ancestorSwitcherTrigger{color:var(--dsw-alias-label-tertiary);font-weight:400}.rhoEmW_switcherTitle{text-overflow:ellipsis;white-space:nowrap;flex:1;min-width:0;overflow:hidden}.rhoEmW_switcherTrigger svg{flex:none}.rhoEmW_separator{color:var(--dsw-alias-label-caption);font-size:14px;line-height:20px}.rhoEmW_activitySlot{flex:none;width:10px;height:10px;display:inline-flex}.rhoEmW_trigger:hover,.rhoEmW_trigger:focus-visible{color:var(--dsw-alias-label-secondary)}.rhoEmW_switcherTrigger:hover,.rhoEmW_switcherTrigger:focus-visible{color:var(--dsw-alias-label-primary)}.rhoEmW_ancestorSwitcherTrigger:hover,.rhoEmW_ancestorSwitcherTrigger:focus-visible{color:var(--dsw-alias-label-tertiary)}.rhoEmW_trigger svg,.rhoEmW_switcherTrigger svg{transition:transform .12s}.rhoEmW_triggerOpen{transform:rotate(180deg)}.rhoEmW_menu{z-index:100;box-sizing:border-box;background:var(--dsw-specific-menu);--dsh-scrollbar-thumb:var(--dsw-alias-scrollbar-bg-l2);--dsh-scrollbar-thumb-hover:var(--dsw-alias-scrollbar-hover-l2);--dsw-elevation-stroke-color:var(--dsw-alias-border-l1);width:336px;max-width:min(400px,100vw - 32px);max-height:min(560px,100vh - 140px);box-shadow:var(--dsw-elevation-prominent);border-radius:20px;flex-direction:column;padding:4px;display:flex;position:fixed;overflow:auto}.rhoEmW_node{min-width:0;position:relative}.rhoEmW_menu>.rhoEmW_node{margin-left:-3px}.rhoEmW_row{box-sizing:border-box;width:100%;min-height:50px;color:var(--dsw-alias-label-primary);text-align:left;cursor:pointer;background:0 0;border:0;border-radius:8px;outline:none;align-items:flex-start;gap:8px;padding:7px 8px 7px 11px;font-size:13px;line-height:18px;display:flex;position:relative}.rhoEmW_row:hover>.rhoEmW_clickarea,.rhoEmW_row:focus-visible>.rhoEmW_clickarea{background:var(--dsw-alias-interactive-bg-hover)}.rhoEmW_clickarea{box-sizing:border-box;border-radius:8px;flex:1;align-self:stretch;align-items:flex-start;gap:8px;min-width:0;margin:-7px -8px;padding:7px 8px;display:flex}.rhoEmW_row>[data-state],.rhoEmW_clickarea>[data-state]{margin-top:4px}.rhoEmW_disabled{color:var(--dsw-alias-label-dimmed);cursor:not-allowed}.rhoEmW_disabled:hover{background:0 0}.rhoEmW_loadingRow{cursor:default}.rhoEmW_disclosure,.rhoEmW_disclosureSpace{flex:none;width:14px;height:18px}.rhoEmW_disclosure{color:var(--dsw-alias-label-tertiary);cursor:pointer;background:0 0;border:0;justify-content:center;align-items:center;padding:0;transition:transform .12s;display:inline-flex}.rhoEmW_disclosure:hover{color:var(--dsw-alias-label-primary)}.rhoEmW_disclosureOpen{transform:rotate(90deg)}.rhoEmW_content{flex-direction:column;flex:1;min-width:0;display:flex}.rhoEmW_label,.rhoEmW_summary{text-overflow:ellipsis;white-space:nowrap;overflow:hidden}.rhoEmW_label{color:inherit;font-weight:400}.rhoEmW_currentLabel{font-weight:600}.rhoEmW_summary,.rhoEmW_metrics{color:var(--dsw-alias-label-tertiary);font-size:11px;line-height:16px}.rhoEmW_metrics{font-variant-numeric:tabular-nums;text-align:right;white-space:nowrap;flex:none;grid-template-rows:18px 16px;display:grid}.rhoEmW_metricToken{grid-row:1;line-height:18px}.rhoEmW_metricDuration{grid-row:2}.rhoEmW_sidebarButton{corner-shape:round;width:28px;height:28px;color:var(--dsw-alias-label-tertiary);cursor:pointer;background:0 0;border:0;border-radius:50%;flex:none;justify-content:center;align-items:center;margin:4px 0;padding:6px;display:inline-flex}.rhoEmW_sidebarButton:hover,.rhoEmW_sidebarButton:focus-visible{background:var(--dsw-alias-interactive-bg-hover);color:var(--dsw-alias-label-primary)}.rhoEmW_children{margin-left:18px;padding-left:4px;position:relative}.rhoEmW_children:before,.rhoEmW_children>.rhoEmW_node:before{content:\"\";border-left:.5px solid var(--dsw-alias-border-l2);position:absolute;left:0}.rhoEmW_children:before{height:26px;top:-26px}.rhoEmW_children[aria-busy=true]:before{content:none}.rhoEmW_children>.rhoEmW_node:before{top:0;bottom:0;left:-4px}.rhoEmW_children>.rhoEmW_node:last-child:before{height:17px;bottom:auto}.rhoEmW_children>.rhoEmW_node>.rhoEmW_row:before{content:\"\";border-top:.5px solid var(--dsw-alias-border-l2);width:14px;position:absolute;top:16px;left:-4px}.rhoEmW_notice,.rhoEmW_error{color:var(--dsw-alias-label-tertiary);padding:10px 12px;font-size:12px;line-height:18px}.rhoEmW_error{color:var(--dsw-alias-state-error-primary);justify-content:space-between;align-items:center;gap:12px;display:flex}.rhoEmW_refresh{color:inherit;cursor:pointer;background:0 0;border:0;border-radius:6px;flex:none;align-items:center;gap:4px;padding:4px 6px;display:inline-flex}.rhoEmW_refresh:hover{background:var(--dsw-alias-interactive-bg-hover)}";
		const tagId$2 = "@deepseek-ai/dsh-client-ui-subagent/SubagentHeaderLineage.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$2) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-subagent";
			tag.dataset.pluginCss = tagId$2;
			tag.textContent = css$2;
			document.head.appendChild(tag);
		}
		var SubagentHeaderLineage_module_css_default = {
			"activitySlot": "rhoEmW_activitySlot",
			"ancestorSwitcherTrigger": "rhoEmW_ancestorSwitcherTrigger",
			"children": "rhoEmW_children",
			"clickarea": "rhoEmW_clickarea",
			"content": "rhoEmW_content",
			"currentLabel": "rhoEmW_currentLabel",
			"disabled": "rhoEmW_disabled",
			"disclosure": "rhoEmW_disclosure",
			"disclosureOpen": "rhoEmW_disclosureOpen",
			"disclosureSpace": "rhoEmW_disclosureSpace",
			"error": "rhoEmW_error",
			"label": "rhoEmW_label",
			"loadingRow": "rhoEmW_loadingRow",
			"menu": "rhoEmW_menu",
			"metricDuration": "rhoEmW_metricDuration",
			"metricToken": "rhoEmW_metricToken",
			"metrics": "rhoEmW_metrics",
			"node": "rhoEmW_node",
			"notice": "rhoEmW_notice",
			"refresh": "rhoEmW_refresh",
			"root": "rhoEmW_root",
			"row": "rhoEmW_row",
			"separator": "rhoEmW_separator",
			"sidebarButton": "rhoEmW_sidebarButton",
			"summary": "rhoEmW_summary",
			"switcherRoot": "rhoEmW_switcherRoot",
			"switcherTitle": "rhoEmW_switcherTitle",
			"switcherTrigger": "rhoEmW_switcherTrigger",
			"trigger": "rhoEmW_trigger",
			"triggerOpen": "rhoEmW_triggerOpen"
		};
		//#endregion
		//#region lib/types/client/subagent-lineage.js
		/** UI Subagent-owned projection of descendant counts from Session summaries. */
		/**
		* Index uninterrupted subagent descendants under each ancestor.
		* @param summaries - Session summaries keyed by id.
		* @returns descendant totals keyed by possible parent id.
		*/
		function indexSubagentDescendants(summaries) {
			const indexed = /* @__PURE__ */ new Map();
			for (const descendant of Object.values(summaries)) {
				if (descendant.origin !== "subagent") continue;
				const seen = /* @__PURE__ */ new Set();
				let current = descendant;
				while (current?.origin === "subagent" && current.parentId !== void 0 && !seen.has(current.id)) {
					seen.add(current.id);
					const aggregate = indexed.get(current.parentId);
					if (aggregate === void 0) indexed.set(current.parentId, {
						count: 1,
						runningCount: descendant.running ? 1 : 0
					});
					else {
						aggregate.count += 1;
						if (descendant.running) aggregate.runningCount += 1;
					}
					current = summaries[current.parentId];
				}
			}
			return indexed;
		}
		//#endregion
		//#region lib/types/client/SubagentHeaderLineage.js
		function diagnosticReason(entry, t) {
			switch (entry.reason) {
				case "corrupt": return t("diagnostic.corrupt");
				case "unsupported": return t("diagnostic.unsupported");
				case "unavailable": return t("diagnostic.unavailable");
			}
		}
		function treeItems(root) {
			return root === null ? [] : Array.from(root.querySelectorAll("[role=\"treeitem\"]:not([aria-disabled=\"true\"])"));
		}
		/** Compact token count shared in shape with the conversation stats strip. */
		function formatTokens(value, t) {
			const scaled = (next) => next >= 100 ? String(Math.round(next)) : String(Math.round(next * 10) / 10);
			if (value < 1e3) return String(value);
			if (value < 1e6) return t("tokens.thousand", { value: scaled(value / 1e3) });
			return t("tokens.million", { value: scaled(value / 1e6) });
		}
		/** Sum the four disjoint durable provider-usage buckets. */
		function tokenTotal(usage) {
			return usage === void 0 ? void 0 : usage.uncachedInputTokens + usage.outputTokens + usage.cacheReadTokens + usage.cacheWriteTokens;
		}
		/** Exact whole-second active-turn duration for one catalog row. */
		function activityDuration(summary, activity, now) {
			if (summary === void 0) return void 0;
			const timing = summary.projectionValues?.subagentTiming;
			if (timing === void 0) return void 0;
			if (timing.active === void 0) return timing.settledMs;
			const end = activity === "running" ? now : timing.active.through;
			return timing.settledMs + Math.max(0, end - timing.active.since);
		}
		function splitDuration(ms) {
			const totalSeconds = Math.floor(Math.max(0, ms) / 1e3);
			const totalMinutes = Math.floor(totalSeconds / 60);
			const totalHours = Math.floor(totalMinutes / 60);
			return {
				seconds: totalSeconds % 60,
				minutes: totalMinutes % 60,
				hours: totalHours % 24,
				days: Math.floor(totalHours / 24),
				totalMinutes,
				totalHours
			};
		}
		/** Format a duration with decreasing visual precision at larger scales. */
		function formatDuration(ms, t) {
			const { seconds, minutes, hours, days, totalMinutes, totalHours } = splitDuration(ms);
			if (days >= 365) {
				const years = Math.floor(days / 365);
				const months = Math.floor(days % 365 / 30);
				return months === 0 ? t("duration.years", { years }) : t("duration.yearsMonths", {
					years,
					months
				});
			}
			if (days >= 30) {
				const months = Math.floor(days / 30);
				const remainingDays = days % 30;
				return remainingDays === 0 ? t("duration.months", { months }) : t("duration.monthsDays", {
					months,
					days: remainingDays
				});
			}
			if (days > 0) return hours === 0 ? t("duration.days", { days }) : t("duration.daysHours", {
				days,
				hours
			});
			if (totalHours > 0) return t("duration.hours", {
				hours: totalHours,
				minutes: String(minutes).padStart(2, "0"),
				seconds: String(seconds).padStart(2, "0")
			});
			if (totalMinutes > 0) return t("duration.minutes", {
				minutes: totalMinutes,
				seconds: String(seconds).padStart(2, "0")
			});
			return t("duration.seconds", { seconds });
		}
		/** Preserve exact whole seconds for hover and accessible naming. */
		function formatExactDuration(ms, t) {
			const { seconds, minutes, hours, days } = splitDuration(ms);
			return days === 0 ? formatDuration(ms, t) : t("duration.exactDays", {
				days,
				hours: String(hours).padStart(2, "0"),
				minutes: String(minutes).padStart(2, "0"),
				seconds: String(seconds).padStart(2, "0")
			});
		}
		const NO_DESCENDANTS = {
			count: 0,
			runningCount: 0
		};
		function SubagentSwitcherIcon() {
			return (0, react_jsx_runtime.jsxs)("svg", {
				width: "16",
				height: "16",
				viewBox: "0 0 20 20",
				fill: "none",
				"aria-hidden": "true",
				children: [(0, react_jsx_runtime.jsx)("path", {
					d: "M5.99951 12.7L8.95546 14.9478C9.40011 15.2859 9.62244 15.455 9.87526 15.488C9.95774 15.4988 10.0413 15.4988 10.1238 15.488C10.3766 15.455 10.5989 15.2859 11.0436 14.9478L13.9995 12.7",
					stroke: "currentColor",
					strokeWidth: "1.5"
				}), (0, react_jsx_runtime.jsx)("path", {
					d: "M13.9995 7.7417L11.0436 5.49387C10.5989 5.15574 10.3766 4.98668 10.1238 4.95362C10.0413 4.94283 9.95775 4.94283 9.87527 4.95362C9.62245 4.98668 9.40012 5.15574 8.95547 5.49387L5.99952 7.7417",
					stroke: "currentColor",
					strokeWidth: "1.5"
				})]
			});
		}
		/** Render the known direct-child shape while its authoritative catalog hydrates. */
		function CatalogLoadingRows({ parentSessionId, summaries, level, t }) {
			const children = Object.values(summaries).filter((summary) => summary.origin === "subagent" && summary.parentId === parentSessionId);
			if (children.length === 0) return (0, react_jsx_runtime.jsx)("div", {
				className: SubagentHeaderLineage_module_css_default.notice,
				children: t("loading.label")
			});
			return children.map((summary) => (0, react_jsx_runtime.jsx)("div", {
				className: SubagentHeaderLineage_module_css_default.node,
				children: (0, react_jsx_runtime.jsxs)("div", {
					role: "treeitem",
					"aria-disabled": "true",
					"aria-level": level,
					"aria-label": t("loading.aria"),
					className: `${SubagentHeaderLineage_module_css_default.row} ${SubagentHeaderLineage_module_css_default.disabled} ${SubagentHeaderLineage_module_css_default.loadingRow}`,
					children: [
						(0, react_jsx_runtime.jsx)("span", { className: SubagentHeaderLineage_module_css_default.disclosureSpace }),
						(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.StateDot, { state: summary.running ? "ongoing" : "done" }),
						(0, react_jsx_runtime.jsx)("span", {
							className: SubagentHeaderLineage_module_css_default.content,
							children: (0, react_jsx_runtime.jsx)("span", {
								className: SubagentHeaderLineage_module_css_default.label,
								children: t("loading.label")
							})
						})
					]
				})
			}, summary.id));
		}
		/** Render one catalog level and recurse only through explicitly expanded rows. */
		function CatalogRows({ parentSessionId, currentSessionId, catalog, catalogs, summaries, expanded, level, now, openChild, openChildAside, refresh, toggleBranch, closeCatalog, t }) {
			const emptyLoading = catalog.state === "loading" && catalog.entries.length === 0;
			const reserveDisclosure = catalog.entries.some((entry) => entry.kind === "child" && entry.hasChildren);
			return (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [
				emptyLoading && (0, react_jsx_runtime.jsx)(CatalogLoadingRows, {
					parentSessionId,
					summaries,
					level,
					t
				}),
				catalog.state === "error" && (0, react_jsx_runtime.jsxs)("div", {
					className: SubagentHeaderLineage_module_css_default.error,
					children: [(0, react_jsx_runtime.jsx)("span", { children: catalog.error?.message ?? t("load.error") }), (0, react_jsx_runtime.jsxs)("button", {
						type: "button",
						className: SubagentHeaderLineage_module_css_default.refresh,
						onClick: () => {
							refresh(parentSessionId);
						},
						children: [(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconRefreshOutline14, {}), t("retry")]
					})]
				}),
				catalog.entries.map((entry) => {
					if (entry.kind === "diagnostic") {
						const reason = diagnosticReason(entry, t);
						return (0, react_jsx_runtime.jsx)("div", {
							className: SubagentHeaderLineage_module_css_default.node,
							children: (0, react_jsx_runtime.jsxs)("div", {
								role: "treeitem",
								"aria-disabled": "true",
								"aria-level": level,
								"aria-label": `${entry.id} ${reason}`,
								className: `${SubagentHeaderLineage_module_css_default.row} ${SubagentHeaderLineage_module_css_default.disabled}`,
								title: reason,
								children: [
									reserveDisclosure && (0, react_jsx_runtime.jsx)("span", { className: SubagentHeaderLineage_module_css_default.disclosureSpace }),
									(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.StateDot, { state: "error" }),
									(0, react_jsx_runtime.jsxs)("span", {
										className: SubagentHeaderLineage_module_css_default.content,
										children: [(0, react_jsx_runtime.jsx)("span", {
											className: SubagentHeaderLineage_module_css_default.label,
											children: entry.id
										}), (0, react_jsx_runtime.jsx)("span", {
											className: SubagentHeaderLineage_module_css_default.summary,
											children: reason
										})]
									})
								]
							})
						}, entry.id);
					}
					const childCatalog = catalogs[entry.id];
					const isCurrent = entry.id === currentSessionId;
					const isExpanded = expanded.has(entry.id);
					const knownLeaf = !entry.hasChildren;
					const childLoading = childCatalog === void 0 || childCatalog.state === "loading" && childCatalog.entries.length === 0;
					const summary = summaries[entry.id];
					const label = entry.label ?? entry.id;
					const mode = entry.mode === "one-shot" ? t("mode.oneShot") : t("mode.continuable");
					const activity = entry.activity === "running" ? t("activity.running") : t("activity.inactive");
					const secondary = [
						summary?.title,
						mode,
						activity
					].filter((value) => value !== void 0).join(" · ");
					const totalTokens = tokenTotal(summary?.projectionValues?.tokenUsage);
					const durationMs = activityDuration(summary, entry.activity, now);
					const tokenMetric = totalTokens === void 0 ? void 0 : t("tokens.total", { value: formatTokens(totalTokens, t) });
					const durationMetric = durationMs === void 0 ? void 0 : {
						compact: formatDuration(durationMs, t),
						exact: formatExactDuration(durationMs, t)
					};
					const metrics = [tokenMetric, durationMetric?.exact].filter((value) => value !== void 0).join(" · ");
					const open = () => {
						openChild({
							parentSessionId,
							childSessionId: entry.id,
							mode: entry.mode
						});
						closeCatalog();
					};
					const openAside = (event) => {
						event.preventDefault();
						event.stopPropagation();
						openChildAside({
							parentSessionId,
							childSessionId: entry.id,
							mode: entry.mode
						});
						closeCatalog();
					};
					const handleKey = (event) => {
						if (event.key === "Enter" || event.key === " ") {
							event.preventDefault();
							event.stopPropagation();
							open();
						} else if (event.key === "ArrowRight" && !knownLeaf && !isExpanded || event.key === "ArrowLeft" && isExpanded) {
							event.preventDefault();
							event.stopPropagation();
							toggleBranch(entry.id);
						}
					};
					const toggle = (event) => {
						event.preventDefault();
						event.stopPropagation();
						toggleBranch(entry.id);
					};
					return (0, react_jsx_runtime.jsxs)("div", {
						className: SubagentHeaderLineage_module_css_default.node,
						children: [(0, react_jsx_runtime.jsxs)("div", {
							role: "treeitem",
							tabIndex: 0,
							"aria-level": level,
							"aria-current": isCurrent || void 0,
							"aria-label": [
								label,
								secondary,
								metrics
							].filter((value) => value !== "").join(" "),
							...knownLeaf ? {} : { "aria-expanded": isExpanded },
							className: SubagentHeaderLineage_module_css_default.row,
							onClick: open,
							onKeyDown: handleKey,
							children: [knownLeaf ? reserveDisclosure && (0, react_jsx_runtime.jsx)("span", { className: SubagentHeaderLineage_module_css_default.disclosureSpace }) : (0, react_jsx_runtime.jsx)("button", {
								type: "button",
								tabIndex: -1,
								className: `${SubagentHeaderLineage_module_css_default.disclosure} ${isExpanded ? SubagentHeaderLineage_module_css_default.disclosureOpen : ""}`,
								"aria-label": t(isExpanded ? "branch.collapse" : "branch.expand", { label }),
								onClick: toggle,
								children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronRightOutline14, {})
							}), (0, react_jsx_runtime.jsxs)("div", {
								className: SubagentHeaderLineage_module_css_default.clickarea,
								children: [
									(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.StateDot, { state: entry.activity === "running" ? "ongoing" : "done" }),
									(0, react_jsx_runtime.jsxs)("span", {
										className: SubagentHeaderLineage_module_css_default.content,
										children: [(0, react_jsx_runtime.jsx)("span", {
											className: `${SubagentHeaderLineage_module_css_default.label} ${isCurrent ? SubagentHeaderLineage_module_css_default.currentLabel : ""}`,
											children: label
										}), (0, react_jsx_runtime.jsx)("span", {
											className: SubagentHeaderLineage_module_css_default.summary,
											children: secondary
										})]
									}),
									metrics !== "" && (0, react_jsx_runtime.jsxs)("span", {
										className: SubagentHeaderLineage_module_css_default.metrics,
										children: [tokenMetric !== void 0 && (0, react_jsx_runtime.jsx)("span", {
											className: SubagentHeaderLineage_module_css_default.metricToken,
											children: tokenMetric
										}), durationMetric !== void 0 && (0, react_jsx_runtime.jsx)("span", {
											className: SubagentHeaderLineage_module_css_default.metricDuration,
											title: t("duration.exactTitle", { duration: durationMetric.exact }),
											children: durationMetric.compact
										})]
									}),
									!isCurrent && (0, react_jsx_runtime.jsx)("button", {
										type: "button",
										className: SubagentHeaderLineage_module_css_default.sidebarButton,
										"aria-label": t("open.sidebar", { label }),
										title: t("open.sidebar", { label }),
										onClick: openAside,
										onKeyDown: (event) => {
											event.stopPropagation();
										},
										children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronRightOutline14, {})
									})
								]
							})]
						}), isExpanded && !knownLeaf && (0, react_jsx_runtime.jsx)("div", {
							role: "group",
							className: SubagentHeaderLineage_module_css_default.children,
							"aria-busy": childLoading || void 0,
							children: childCatalog === void 0 ? (0, react_jsx_runtime.jsx)(CatalogLoadingRows, {
								parentSessionId: entry.id,
								summaries,
								level: level + 1,
								t
							}) : (0, react_jsx_runtime.jsx)(CatalogRows, {
								parentSessionId: entry.id,
								currentSessionId,
								catalog: childCatalog,
								catalogs,
								summaries,
								expanded,
								level: level + 1,
								now,
								openChild,
								openChildAside,
								refresh,
								toggleBranch,
								closeCatalog,
								t
							})
						})]
					}, entry.id);
				})
			] });
		}
		const MENU_VIEWPORT_MARGIN = 16;
		/** Place a portaled catalog below its trigger without crossing the viewport edge. */
		function catalogMenuPosition(trigger) {
			const rect = trigger.getBoundingClientRect();
			const width = Math.min(336, window.innerWidth - MENU_VIEWPORT_MARGIN * 2);
			return {
				top: rect.bottom + 5,
				left: Math.min(Math.max(MENU_VIEWPORT_MARGIN, rect.left), window.innerWidth - width - MENU_VIEWPORT_MARGIN)
			};
		}
		/** One trigger-plus-tree dropdown over the catalog rooted at `rootSessionId`. */
		function CatalogDropdown({ rootSessionId, currentSessionId, displayTitle, openTitle, variant, separator = false, useSessions, openChild, openChildAside, refresh, setCatalogOpen, t }) {
			const ancestorSwitcher = variant === "switcher" && openTitle !== void 0;
			const catalogs = useSessions((state) => state.subagentsByParent);
			const summaries = useSessions((state) => state.byId);
			const catalog = catalogs[rootSessionId];
			const [open, setOpen] = (0, react.useState)(false);
			const [menuPosition, setMenuPosition] = (0, react.useState)();
			const [now, setNow] = (0, react.useState)(() => Date.now());
			const [expanded, setExpanded] = (0, react.useState)(() => /* @__PURE__ */ new Set());
			const rootRef = (0, react.useRef)(null);
			const triggerRef = (0, react.useRef)(null);
			const menuRef = (0, react.useRef)(null);
			const hoverOpenTimer = (0, react.useRef)(void 0);
			const hoverCloseTimer = (0, react.useRef)(void 0);
			const observedCatalogs = (0, react.useRef)(/* @__PURE__ */ new Set());
			const setCatalogOpenRef = (0, react.useRef)(setCatalogOpen);
			setCatalogOpenRef.current = setCatalogOpen;
			const currentEntry = currentSessionId === void 0 ? void 0 : catalog?.entries.find((entry) => entry.kind === "child" && entry.id === currentSessionId);
			const switcherDisplayTitle = currentEntry?.kind === "child" ? currentEntry.label ?? currentEntry.id : displayTitle;
			const healthy = catalog?.entries.filter((entry) => entry.kind === "child") ?? [];
			const descendants = (0, react.useMemo)(() => indexSubagentDescendants(summaries).get(rootSessionId) ?? NO_DESCENDANTS, [rootSessionId, summaries]);
			const descendantCount = Math.max(healthy.length, descendants.count);
			const totalCountKey = descendantCount === 1 ? "count.total.one" : "count.total.other";
			const runningCountKey = descendants.runningCount === 1 ? "count.running.one" : "count.running.other";
			const presentedCatalog = (descendants.count > 0 || variant === "switcher") && (catalog === void 0 || catalog.state === "ready" && catalog.entries.length === 0) ? {
				entries: [],
				parentAvailable: catalog?.parentAvailable ?? false,
				state: "loading",
				error: null
			} : catalog;
			const observeCatalog = (parentSessionId, next) => {
				if (next) observedCatalogs.current.add(parentSessionId);
				else observedCatalogs.current.delete(parentSessionId);
				setCatalogOpen(parentSessionId, next);
			};
			const closeAllCatalogs = () => {
				for (const parentSessionId of observedCatalogs.current) setCatalogOpen(parentSessionId, false);
				observedCatalogs.current.clear();
				setExpanded(/* @__PURE__ */ new Set());
			};
			const cancelHoverClose = () => {
				if (hoverCloseTimer.current === void 0) return;
				clearTimeout(hoverCloseTimer.current);
				hoverCloseTimer.current = void 0;
			};
			const cancelHoverOpen = () => {
				if (hoverOpenTimer.current === void 0) return;
				clearTimeout(hoverOpenTimer.current);
				hoverOpenTimer.current = void 0;
			};
			const changeOpen = (next, restoreFocus = false) => {
				cancelHoverOpen();
				cancelHoverClose();
				if (next) {
					const trigger = triggerRef.current;
					/* v8 ignore next -- a queued callback can outlive the trigger */
					if (trigger === null) return;
					setOpen(true);
					setMenuPosition(catalogMenuPosition(trigger));
					setNow(Date.now());
					observeCatalog(rootSessionId, true);
				} else {
					setOpen(false);
					setMenuPosition(void 0);
					closeAllCatalogs();
				}
				if (restoreFocus) queueMicrotask(() => {
					triggerRef.current?.focus();
				});
			};
			const scheduleHoverOpen = () => {
				cancelHoverOpen();
				cancelHoverClose();
				if (open) return;
				hoverOpenTimer.current = setTimeout(() => {
					hoverOpenTimer.current = void 0;
					changeOpen(true);
				}, 150);
			};
			const scheduleHoverClose = () => {
				cancelHoverOpen();
				cancelHoverClose();
				hoverCloseTimer.current = setTimeout(() => {
					hoverCloseTimer.current = void 0;
					changeOpen(false);
				}, 120);
			};
			const closeBranch = (root) => {
				const closing = /* @__PURE__ */ new Set();
				const visit = (parentSessionId) => {
					if (closing.has(parentSessionId) || !expanded.has(parentSessionId)) return;
					closing.add(parentSessionId);
					const branch = catalogs[parentSessionId];
					for (const entry of branch?.entries ?? []) if (entry.kind === "child") visit(entry.id);
				};
				visit(root);
				for (const parentSessionId of closing) observeCatalog(parentSessionId, false);
				setExpanded((current) => new Set([...current].filter((id) => !closing.has(id))));
			};
			const toggleBranch = (childSessionId) => {
				if (expanded.has(childSessionId)) {
					closeBranch(childSessionId);
					return;
				}
				setExpanded((current) => new Set(current).add(childSessionId));
				observeCatalog(childSessionId, true);
			};
			(0, react.useEffect)(() => {
				if (!open) return;
				const closeOutside = (event) => {
					if (event.target instanceof Node && !rootRef.current?.contains(event.target) && !menuRef.current?.contains(event.target)) changeOpen(false);
				};
				document.addEventListener("pointerdown", closeOutside);
				return () => {
					document.removeEventListener("pointerdown", closeOutside);
				};
			}, [open]);
			(0, react.useEffect)(() => {
				if (!open) return;
				const placeMenu = () => {
					const trigger = triggerRef.current;
					/* v8 ignore next -- native resize or scroll can outlive the trigger */
					if (trigger === null) return;
					setMenuPosition(catalogMenuPosition(trigger));
				};
				window.addEventListener("resize", placeMenu);
				document.addEventListener("scroll", placeMenu, true);
				return () => {
					window.removeEventListener("resize", placeMenu);
					document.removeEventListener("scroll", placeMenu, true);
				};
			}, [open]);
			(0, react.useEffect)(() => {
				if (!open || descendants.runningCount === 0) return;
				const timer = setInterval(() => {
					setNow(Date.now());
				}, 1e3);
				return () => {
					clearInterval(timer);
				};
			}, [open, descendants.runningCount]);
			(0, react.useEffect)(() => () => {
				cancelHoverOpen();
				cancelHoverClose();
				for (const parentSessionId of observedCatalogs.current) setCatalogOpenRef.current(parentSessionId, false);
				observedCatalogs.current.clear();
			}, []);
			const visible = presentedCatalog !== void 0 && (variant === "switcher" || presentedCatalog.state === "error" || presentedCatalog.entries.length > 0 || descendantCount > 0);
			(0, react.useEffect)(() => {
				if (visible) return;
				cancelHoverOpen();
				cancelHoverClose();
				if (!open) return;
				setOpen(false);
				closeAllCatalogs();
			}, [visible, open]);
			if (!visible) return null;
			const focusAt = (index) => {
				const items = treeItems(menuRef.current);
				if (items.length === 0) return;
				items[(index + items.length) % items.length]?.focus();
			};
			const navigate = (event) => {
				const items = treeItems(menuRef.current);
				const index = items.indexOf(document.activeElement);
				if (event.key === "Escape") {
					event.preventDefault();
					changeOpen(false, true);
				} else if (event.key === "Home") {
					event.preventDefault();
					focusAt(0);
				} else if (event.key === "End") {
					event.preventDefault();
					focusAt(items.length - 1);
				} else if (event.key === "ArrowDown") {
					event.preventDefault();
					focusAt(index + 1);
				} else if (event.key === "ArrowUp") {
					event.preventDefault();
					focusAt(index < 0 ? items.length - 1 : index - 1);
				}
			};
			return (0, react_jsx_runtime.jsxs)("div", {
				className: `${SubagentHeaderLineage_module_css_default.root} ${variant === "switcher" ? SubagentHeaderLineage_module_css_default.switcherRoot : ""}`,
				ref: rootRef,
				onKeyDown: navigate,
				onMouseEnter: scheduleHoverOpen,
				onMouseLeave: scheduleHoverClose,
				children: [
					separator && (0, react_jsx_runtime.jsx)("span", {
						className: SubagentHeaderLineage_module_css_default.separator,
						children: "/"
					}),
					(0, react_jsx_runtime.jsxs)("button", {
						ref: triggerRef,
						type: "button",
						className: variant === "switcher" ? `${SubagentHeaderLineage_module_css_default.switcherTrigger} ${ancestorSwitcher ? SubagentHeaderLineage_module_css_default.ancestorSwitcherTrigger : ""}` : SubagentHeaderLineage_module_css_default.trigger,
						"aria-haspopup": "tree",
						"aria-expanded": open,
						"aria-label": variant === "switcher" ? t("switcher.aria", { title: switcherDisplayTitle }) : t(descendants.runningCount > 0 ? runningCountKey : totalCountKey, { count: descendants.runningCount > 0 ? descendants.runningCount : descendantCount }),
						onClick: openTitle === void 0 ? void 0 : () => {
							cancelHoverOpen();
							if (open) changeOpen(false);
							openTitle();
						},
						onKeyDown: (event) => {
							if (event.key !== "ArrowDown") return;
							event.preventDefault();
							if (!open) changeOpen(true);
							queueMicrotask(() => {
								focusAt(0);
							});
						},
						children: [variant === "switcher" ? (0, react_jsx_runtime.jsx)("span", {
							className: SubagentHeaderLineage_module_css_default.switcherTitle,
							children: switcherDisplayTitle
						}) : (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [descendants.runningCount > 0 && (0, react_jsx_runtime.jsx)("span", {
							className: SubagentHeaderLineage_module_css_default.activitySlot,
							children: (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.StateDot, { state: "ongoing" })
						}), (0, react_jsx_runtime.jsx)("span", {
							className: SubagentHeaderLineage_module_css_default.count,
							children: t(totalCountKey, { count: descendantCount })
						})] }), variant === "switcher" ? (0, react_jsx_runtime.jsx)(SubagentSwitcherIcon, {}) : (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconChevronDownOutline14, { className: open ? SubagentHeaderLineage_module_css_default.triggerOpen : void 0 })]
					}),
					open && (0, react_dom.createPortal)((0, react_jsx_runtime.jsx)("div", {
						ref: menuRef,
						className: SubagentHeaderLineage_module_css_default.menu,
						style: menuPosition,
						role: "tree",
						"aria-label": t("tree.aria"),
						onMouseEnter: cancelHoverClose,
						onMouseLeave: scheduleHoverClose,
						children: (0, react_jsx_runtime.jsx)(CatalogRows, {
							parentSessionId: rootSessionId,
							currentSessionId,
							catalog: presentedCatalog,
							catalogs,
							summaries,
							expanded,
							level: 1,
							now,
							openChild,
							openChildAside,
							refresh,
							toggleBranch,
							closeCatalog: () => {
								changeOpen(false);
							},
							t
						})
					}), document.body)
				]
			});
		}
		/**
		* Render one breadcrumb title together with its subagent navigation.
		* @param props - Breadcrumb title, session standard props, and catalog actions.
		* @returns An ordinary-title descendant count, or a title-and-chevron sibling switcher.
		*/
		function SubagentHeaderLineage({ lineageSessionId, displayTitle, openTitle, useSessions, openChild, openChildAside, refresh, setCatalogOpen, t }) {
			const parentId = useSessions((state) => {
				const summary = state.byId[lineageSessionId];
				return summary?.origin === "subagent" ? summary.parentId : void 0;
			});
			const shared = {
				useSessions,
				openChild,
				openChildAside,
				refresh,
				setCatalogOpen,
				t
			};
			if (parentId === void 0) return (0, react_jsx_runtime.jsx)(CatalogDropdown, {
				rootSessionId: lineageSessionId,
				variant: "count",
				separator: true,
				...shared
			}, lineageSessionId);
			return (0, react_jsx_runtime.jsxs)(react_jsx_runtime.Fragment, { children: [(0, react_jsx_runtime.jsx)(CatalogDropdown, {
				rootSessionId: parentId,
				currentSessionId: lineageSessionId,
				variant: "switcher",
				displayTitle,
				...openTitle === void 0 ? {} : { openTitle },
				...shared
			}, lineageSessionId), openTitle === void 0 && (0, react_jsx_runtime.jsx)(CatalogDropdown, {
				rootSessionId: lineageSessionId,
				variant: "count",
				...shared
			}, lineageSessionId)] });
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-subagent/src/client/SubagentReadOnlyComposer.module.css.mjs
		const css$1 = "._4WNnAW_frame{border:.5px solid var(--dsw-alias-border-l4);background:var(--dsw-alias-bg-layer-1);min-height:54px;color:var(--dsw-alias-label-tertiary);border-radius:14px;justify-content:center;align-items:center;gap:8px;margin:0 24px 20px;padding:10px 16px;font-size:13px;line-height:20px;display:flex}._4WNnAW_frame strong{color:var(--dsw-alias-label-primary);font-weight:510}";
		const tagId$1 = "@deepseek-ai/dsh-client-ui-subagent/SubagentReadOnlyComposer.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId$1) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-subagent";
			tag.dataset.pluginCss = tagId$1;
			tag.textContent = css$1;
			document.head.appendChild(tag);
		}
		var SubagentReadOnlyComposer_module_css_default = { "frame": "_4WNnAW_frame" };
		//#endregion
		//#region lib/types/client/SubagentReadOnlyComposer.js
		/**
		* Explain why the normal composer is unavailable for an addressed child.
		* @param props - selector-owned read-only reason plus standard slot props.
		* @returns A read-only composer replacement.
		*/
		function SubagentReadOnlyComposer({ matched, t }) {
			const oneShot = matched.reason === "one-shot";
			return (0, react_jsx_runtime.jsxs)("div", {
				className: SubagentReadOnlyComposer_module_css_default.frame,
				role: "status",
				children: [(0, react_jsx_runtime.jsx)("strong", { children: t(oneShot ? "readonly.oneShot.title" : "readonly.title") }), (0, react_jsx_runtime.jsx)("span", { children: t(oneShot ? "readonly.oneShot.body" : "readonly.body") })]
			});
		}
		//#endregion
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-subagent/src/client/sidebar-chat/SidebarChat.module.css.mjs
		const css = "._9lFpZq_root{width:100%;min-width:0;height:100%;min-height:0;display:flex}";
		const tagId = "@deepseek-ai/dsh-client-ui-subagent/SidebarChat.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-subagent";
			tag.dataset.pluginCss = tagId;
			tag.textContent = css;
			document.head.appendChild(tag);
		}
		var SidebarChat_module_css_default = { "root": "_9lFpZq_root" };
		//#endregion
		//#region lib/types/client/sidebar-chat/index.js
		/** Stable implementation identity for the Sidebar tab body. */
		const SUBAGENT_CHAT_ID = "@deepseek-ai/dsh-client-ui-subagent";
		/** Resource-address prefix for an embedded Session chat. */
		const SUBAGENT_CHAT_ADDRESS = "dsh-resource://subagentchat/session/";
		/**
		* Address one subagent Session together with the routing facts needed to restore it.
		* @param address - durable direct-parent subagent address.
		* @returns canonical Sidebar resource address.
		*/
		function subagentChatAddress(address) {
			const query = new URLSearchParams({
				parent: address.parentSessionId,
				mode: address.mode
			});
			return `${SUBAGENT_CHAT_ADDRESS}${encodeURIComponent(address.childSessionId)}?${query}`;
		}
		/**
		* Parse one canonical Sidebar chat resource address.
		* @param value - possible chat resource address.
		* @returns the encoded direct-parent address, or undefined for another or malformed resource.
		*/
		function parseSubagentChatAddress(value) {
			let url;
			try {
				url = new URL(value);
			} catch (_invalidUrl) {
				return;
			}
			if (url.protocol !== "dsh-resource:" || url.hostname.toLowerCase() !== "subagentchat") return void 0;
			const parts = url.pathname.split("/").filter(Boolean);
			if (parts.length !== 2 || parts[0] !== "session") return void 0;
			const parentSessionId = url.searchParams.get("parent");
			const mode = url.searchParams.get("mode");
			if (parentSessionId === null || parentSessionId === "" || mode !== "one-shot" && mode !== "continuable") return;
			try {
				return {
					parentSessionId,
					childSessionId: decodeURIComponent(parts[1]),
					mode
				};
			} catch (_invalidEncoding) {
				return;
			}
		}
		function waitForAbort(signal) {
			if (signal.aborted) return Promise.resolve();
			return new Promise((resolve) => {
				signal.addEventListener("abort", () => {
					resolve();
				}, { once: true });
			});
		}
		function isAbortRequested(signal) {
			return signal.aborted;
		}
		function subagentChatResourceProvider(sessions) {
			return {
				protocol: "subagentchat",
				async *open(resourceAddress, { signal }) {
					const address = parseSubagentChatAddress(resourceAddress);
					if (address === void 0) throw new Error(`ui-subagent: invalid chat resource address "${resourceAddress}"`);
					if (isAbortRequested(signal)) return;
					await sessions.refreshSubagents(address.parentSessionId);
					if (isAbortRequested(signal)) return;
					const reference = sessions.retain(address, {
						source: "sidebarChat",
						signal
					});
					try {
						yield {
							ok: true,
							value: {
								address,
								reference
							}
						};
						await waitForAbort(signal);
					} finally {
						reference.release();
					}
				}
			};
		}
		/** Fixed Chat selection used by an embedded Conversation occurrence. */
		function FixedChatConversationView(props) {
			return (0, react_jsx_runtime.jsx)(react_jsx_runtime.Fragment, { children: props.renderSlot("conversation.session", { view: "chat" }) });
		}
		/** Render the shared Conversation content for one explicitly provided child Session. */
		function ConversationSlotPanel({ sessionId, useSession, useConversation, useSessions, renderFactorySlot }) {
			const session = useSession((value) => value);
			const shellPhase = useConversation((value) => value).activeTargets.size > 0 || !session.blank && !session.awaitingFirstTurn || session.running ? "active" : session.promptAttempted ? "engaging" : "blank";
			const summaryBlank = useSessions((state) => state.byId[sessionId]?.blank);
			const parentAvailabilityPending = session.subagent?.address.mode === "continuable" && session.subagent.parentAvailable === void 0;
			const settling = shellPhase === "blank" && session.openState === "loading" && summaryBlank !== true || parentAvailabilityPending;
			const hero = shellPhase === "blank" && (session.openState === "open" || summaryBlank === true);
			return renderFactorySlot("conversation.content", {
				variant: "embedded",
				phase: settling ? "settling" : hero ? "hero" : "active",
				hero
			}, { slots: { views: FixedChatConversationView } });
		}
		/** Bind a chat resource's child reference around its Conversation slot. */
		function SidebarChatTab({ useResource, useTabInfo, SessionProvider, renderSlot }) {
			const { tab } = useTabInfo();
			const resource = useResource(tab.contentId);
			return (0, react_jsx_runtime.jsx)("div", {
				className: SidebarChat_module_css_default.root,
				"data-sidebar-chat": "",
				children: resource.value === void 0 ? null : (0, react_jsx_runtime.jsx)(SessionProvider, {
					session: resource.value.reference,
					children: renderSlot("sidebar.chat.conversation", {})
				})
			});
		}
		/**
		* Register the chat resource owner and its right-Sidebar presentation.
		* @param ctx - Client root carrying Sessions, resources, Slots, and Sidebar registries.
		* @param t - Chat namespace translator used for fallback tab titles.
		*/
		function registerSidebarChat(ctx, t) {
			ctx.effect(() => ctx.resources.register(subagentChatResourceProvider(ctx.sessions)), "ui-subagent: Sidebar chat resources");
			ctx.effect(() => ctx.sidebarRightTabs.register({
				id: SUBAGENT_CHAT_ID,
				kind: "subagentchat",
				patterns: [`${SUBAGENT_CHAT_ADDRESS}**`],
				priority: "builtin",
				canOpen: (address) => parseSubagentChatAddress(address) !== void 0,
				title: (address) => {
					const child = parseSubagentChatAddress(address)?.childSessionId;
					return child === void 0 ? t("sidebar.chat") : ctx.sessions.list.getSnapshot().byId[child]?.projectionValues?.subagent?.label ?? child;
				}
			}), "ui-subagent: Sidebar chat type");
			ctx.effect(() => ctx.slots.inject("sidebar.right.pane.tab", () => ctx.slots.register({
				name: "sidebar.right.pane.tab",
				key: SUBAGENT_CHAT_ID,
				children: { "sidebar.chat.conversation": {
					kind: "single",
					scope: "session"
				} }
			}, SidebarChatTab)), "ui-subagent: Sidebar chat body");
			ctx.effect(() => ctx.slots.inject("sidebar.chat.conversation", () => ctx.slots.register({ name: "sidebar.chat.conversation" }, ConversationSlotPanel)), "ui-subagent: Sidebar Conversation");
		}
		//#endregion
		//#region lib/types/client/locales.js
		/** `subagent` namespace dictionaries. */
		/** Dictionary namespace owned by this plugin. */
		const NS = "subagent";
		/** Simplified Chinese dictionary (the key-set source of truth). */
		const zh = {
			"diagnostic.corrupt": "会话记录损坏",
			"diagnostic.unsupported": "子代理记录版本不受支持",
			"diagnostic.unavailable": "会话记录暂不可用",
			"duration.seconds": "{seconds}秒",
			"duration.minutes": "{minutes}分{seconds}秒",
			"duration.hours": "{hours}小时{minutes}分{seconds}秒",
			"duration.days": "{days}天",
			"duration.daysHours": "{days}天{hours}小时",
			"duration.months": "约{months}个月",
			"duration.monthsDays": "约{months}个月{days}天",
			"duration.years": "约{years}年",
			"duration.yearsMonths": "约{years}年{months}个月",
			"duration.exactDays": "{days}天{hours}小时{minutes}分{seconds}秒",
			"duration.exactTitle": "总活跃耗时：{duration}",
			"tokens.thousand": "{value}K",
			"tokens.million": "{value}M",
			"tokens.total": "{value} tok",
			"loading.label": "正在加载子代理…",
			"loading.aria": "正在加载子代理",
			"load.error": "无法加载子代理",
			"retry": "重试",
			"mode.oneShot": "一次性",
			"mode.continuable": "可继续",
			"activity.running": "正在运行",
			"activity.inactive": "当前未运行",
			"branch.collapse": "收起 {label} 的下级子代理",
			"branch.expand": "展开 {label} 的下级子代理",
			"count.total.one": "{count} 个子代理",
			"count.total.other": "{count} 个子代理",
			"count.running.one": "{count} 个子代理，正在运行",
			"count.running.other": "{count} 个子代理，正在运行",
			"switcher.aria": "切换子代理：{title}",
			"tree.aria": "子代理会话",
			"open.sidebar": "在侧边栏打开 {label}",
			"sidebar.chat": "聊天",
			"readonly.oneShot.title": "一次性子代理记录",
			"readonly.title": "此子代理暂时只读",
			"readonly.oneShot.body": "一次性任务不支持后续消息，可在这里查看完整执行记录。",
			"readonly.body": "父会话当前不在线，重新打开父会话后即可继续发送消息。"
		};
		/** English dictionary, key-identical to the Chinese source of truth. */
		const en = {
			"diagnostic.corrupt": "corrupted session record",
			"diagnostic.unsupported": "unsupported subagent record version",
			"diagnostic.unavailable": "session record temporarily unavailable",
			"duration.seconds": "{seconds}s",
			"duration.minutes": "{minutes}m {seconds}s",
			"duration.hours": "{hours}h {minutes}m {seconds}s",
			"duration.days": "{days}d",
			"duration.daysHours": "{days}d {hours}h",
			"duration.months": "~{months}mo",
			"duration.monthsDays": "~{months}mo {days}d",
			"duration.years": "~{years}y",
			"duration.yearsMonths": "~{years}y {months}mo",
			"duration.exactDays": "{days}d {hours}h {minutes}m {seconds}s",
			"duration.exactTitle": "Total active duration: {duration}",
			"tokens.thousand": "{value}K",
			"tokens.million": "{value}M",
			"tokens.total": "{value} tok",
			"loading.label": "Loading subagents…",
			"loading.aria": "Loading subagents",
			"load.error": "Unable to load subagents",
			"retry": "Retry",
			"mode.oneShot": "one-shot",
			"mode.continuable": "continuable",
			"activity.running": "running",
			"activity.inactive": "not running",
			"branch.collapse": "Collapse {label} descendants",
			"branch.expand": "Expand {label} descendants",
			"count.total.one": "{count} subagent",
			"count.total.other": "{count} subagents",
			"count.running.one": "{count} subagent running",
			"count.running.other": "{count} subagents running",
			"switcher.aria": "Switch subagent: {title}",
			"tree.aria": "Subagent sessions",
			"open.sidebar": "Open {label} in sidebar",
			"sidebar.chat": "Chat",
			"readonly.oneShot.title": "One-shot subagent record",
			"readonly.title": "This subagent is read-only for now",
			"readonly.oneShot.body": "One-shot tasks do not accept follow-ups; review the full execution record here.",
			"readonly.body": "The parent session is offline; reopen it to continue sending messages."
		};
		//#endregion
		//#region lib/types/client/index.js
		/** Required services for subagent presentation and navigation. */
		const inject = [
			"sessions",
			"uiWorkspace",
			"slots",
			"locale",
			"sidebarRight"
		];
		/** Claim the composer for one-shot history or an unavailable continuation owner. */
		function selectReadOnlySubagent(owner) {
			const subagent = owner.session?.subagent;
			if (subagent === void 0 || subagent === null) return null;
			if (subagent.address.mode === "one-shot") return { reason: "one-shot" };
			if (subagent.parentAvailable !== false) return null;
			return owner.session?.running === true ? null : { reason: "parent-unavailable" };
		}
		/**
		* Client plugin body: register the subagent catalog and read-only composer seats.
		* @param ctx - client root context.
		*/
		function apply(ctx) {
			ctx.effect(() => ctx.locale.register(NS, {
				zh,
				en
			}), "ui-subagent: dictionaries");
			ctx.inject(["resources", "sidebarRightTabs"], (scope) => {
				registerSidebarChat(scope, ctx.locale.bind(NS));
			});
			const sessions = ctx.sessions;
			const catalogActions = (_parentSessionId) => ({
				openChild(address) {
					ctx.uiWorkspace.openSession(address);
				},
				openChildAside(address) {
					ctx.sidebarRight.openResource(subagentChatAddress(address), {
						kind: "subagentchat",
						preferNewPane: true
					});
				},
				refresh(parentSessionId) {
					sessions.refreshSubagents(parentSessionId);
				},
				setCatalogOpen(parentSessionId, open) {
					sessions.setSubagentCatalogOpen(parentSessionId, open);
				}
			});
			ctx.slots.inject("conversation.session.header.lineage", () => ctx.slots.register({
				name: "conversation.session.header.lineage",
				locale: NS,
				inject: catalogActions
			}, SubagentHeaderLineage));
			ctx.slots.inject("conversation.composer", () => ctx.slots.register({
				name: "conversation.composer",
				priority: -10,
				locale: NS,
				select: selectReadOnlySubagent
			}, SubagentReadOnlyComposer));
		}
		//#endregion
		exports.apply = apply;
		exports.inject = inject;
		return module.exports;
	}
});

//# sourceMappingURL=client.js.map