window.__ModuleLoader__.load({
	id: "@deepseek-ai/dsh-client-ui-settings-unarchive-sessions",
	factory: (require) => {
		var module = { exports: {} };
		var exports = module.exports;
		Object.defineProperty(exports, Symbol.toStringTag, { value: "Module" });
		let react_jsx_runtime = require("react/jsx-runtime");
		let react = require("react");
		let _deepseek_ai_dsh_client_ui_primitives = require("@deepseek-ai/dsh-client-ui-primitives");
		//#region \0dsh-css:/private/tmp/dsh-src/packages/client/ui-settings-unarchive-sessions/src/client/ArchivedSessionsSection.module.css.mjs
		const css = ".oJd3ma_section{width:100%;max-width:760px;color:var(--dsw-alias-label-primary);flex-direction:column;gap:12px;display:flex}.oJd3ma_status{color:var(--dsw-alias-label-tertiary);margin:0;font-size:13px;line-height:20px}.oJd3ma_search{width:100%;color:var(--dsw-alias-label-tertiary);align-items:center;display:flex;position:relative}.oJd3ma_search>svg{pointer-events:none;position:absolute;left:12px}.oJd3ma_search input{border:.5px solid var(--dsw-alias-border-l2);background:var(--dsw-alias-bg-base);width:100%;height:32px;color:var(--dsw-alias-label-primary);font:inherit;border-radius:8px;padding:0 12px 0 36px}.oJd3ma_list{flex-direction:column;gap:2px;margin:0;padding:0;list-style:none;display:flex}.oJd3ma_row{border-radius:8px;align-items:center;gap:12px;padding:8px 10px;display:flex}.oJd3ma_row:hover{background:var(--dsw-alias-bg-layer-1)}.oJd3ma_identity{flex-direction:column;flex:1;gap:2px;min-width:0;display:flex}.oJd3ma_title{text-overflow:ellipsis;white-space:nowrap;font-size:13px;line-height:20px;overflow:hidden}.oJd3ma_meta{color:var(--dsw-alias-label-tertiary);font-size:12px;line-height:18px}";
		const tagId = "@deepseek-ai/dsh-client-ui-settings-unarchive-sessions/ArchivedSessionsSection.module.css";
		if (typeof document !== "undefined" && document.querySelector("style[data-plugin-css=" + JSON.stringify(tagId) + "]") === null) {
			const tag = document.createElement("style");
			tag.dataset.plugin = "@deepseek-ai/dsh-client-ui-settings-unarchive-sessions";
			tag.dataset.pluginCss = tagId;
			tag.textContent = css;
			document.head.appendChild(tag);
		}
		var ArchivedSessionsSection_module_css_default = {
			"identity": "oJd3ma_identity",
			"list": "oJd3ma_list",
			"meta": "oJd3ma_meta",
			"row": "oJd3ma_row",
			"search": "oJd3ma_search",
			"section": "oJd3ma_section",
			"status": "oJd3ma_status",
			"title": "oJd3ma_title"
		};
		//#endregion
		//#region lib/types/client/ArchivedSessionsSection.js
		/**
		* Archived-session Settings page: the registry-global archive set joined with
		* the loaded Session summaries, newest archive first, filtered by one search
		* box, with one Unarchive action per row. An archive entry whose Session is
		* gone has no row and no action; the set itself stays host-owned.
		*/
		/** Localized compact relative time of one row's last activity. */
		function timeLabel(updatedAt, now, t) {
			const { unit, n } = (0, _deepseek_ai_dsh_client_ui_primitives.relativeTime)(updatedAt, now);
			return unit === "now" ? t("time.now") : t(`time.${unit}`, { n });
		}
		/** Whether one row matches the normalized query in its title or Workspace label. */
		function matches(row, normalizedQuery) {
			return normalizedQuery.length === 0 || row.title.toLowerCase().includes(normalizedQuery) || row.workspace.toLowerCase().includes(normalizedQuery);
		}
		/**
		* Render the archived-session page.
		* @param props - composed slot props (see {@link ArchivedSessionsSectionProps}).
		* @returns the settings page element tree.
		*/
		function ArchivedSessionsSection(props) {
			const { t, unarchive, useSessions, useWorkspaces } = props;
			const sessions = useSessions((state) => state);
			const workspaces = useWorkspaces((state) => state.items);
			const archivedSessionIds = useWorkspaces((state) => state.archivedSessionIds);
			const [query, setQuery] = (0, react.useState)("");
			const ungrouped = t("ungrouped");
			const summaries = sessions.byId;
			const rows = (0, react.useMemo)(() => {
				const owners = /* @__PURE__ */ new Map();
				for (const workspace of workspaces) for (const id of workspace.sessionIds) owners.set(id, workspace.title);
				return [...archivedSessionIds].reverse().flatMap((id) => {
					const summary = summaries[id];
					if (summary === void 0) return [];
					return [{
						id,
						title: summary.displayTitle,
						workspace: owners.get(id) ?? ungrouped,
						updatedAt: summary.updatedAt
					}];
				});
			}, [
				archivedSessionIds,
				workspaces,
				summaries,
				ungrouped
			]);
			if (sessions.phase !== "ready") return (0, react_jsx_runtime.jsx)("p", {
				className: ArchivedSessionsSection_module_css_default.status,
				children: t("loading")
			});
			const now = Date.now();
			const visible = rows.filter((row) => matches(row, query.trim().toLowerCase()));
			const archived = archivedSessionIds.length > 0;
			return (0, react_jsx_runtime.jsxs)("div", {
				className: ArchivedSessionsSection_module_css_default.section,
				children: [
					(0, react_jsx_runtime.jsxs)("div", {
						className: ArchivedSessionsSection_module_css_default.search,
						children: [(0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.IconSearchOutline16, { "aria-hidden": "true" }), (0, react_jsx_runtime.jsx)("input", {
							type: "search",
							value: query,
							placeholder: t("search"),
							"aria-label": t("search"),
							onChange: (event) => {
								setQuery(event.currentTarget.value);
							}
						})]
					}),
					!archived ? (0, react_jsx_runtime.jsx)("p", {
						className: ArchivedSessionsSection_module_css_default.status,
						children: t("empty")
					}) : null,
					archived && rows.length === 0 ? (0, react_jsx_runtime.jsx)("p", {
						className: ArchivedSessionsSection_module_css_default.status,
						children: t("unavailable")
					}) : null,
					rows.length > 0 && visible.length === 0 ? (0, react_jsx_runtime.jsx)("p", {
						className: ArchivedSessionsSection_module_css_default.status,
						children: t("emptySearch")
					}) : null,
					visible.length > 0 ? (0, react_jsx_runtime.jsx)("ul", {
						className: ArchivedSessionsSection_module_css_default.list,
						children: visible.map((row) => (0, react_jsx_runtime.jsxs)("li", {
							className: ArchivedSessionsSection_module_css_default.row,
							children: [(0, react_jsx_runtime.jsxs)("span", {
								className: ArchivedSessionsSection_module_css_default.identity,
								children: [(0, react_jsx_runtime.jsx)("span", {
									className: ArchivedSessionsSection_module_css_default.title,
									children: row.title
								}), (0, react_jsx_runtime.jsx)("span", {
									className: ArchivedSessionsSection_module_css_default.meta,
									children: [row.workspace, timeLabel(row.updatedAt, now, t)].join(" · ")
								})]
							}), (0, react_jsx_runtime.jsx)(_deepseek_ai_dsh_client_ui_primitives.Button, {
								variant: "outline",
								size: "sm",
								"aria-label": t("unarchiveNamed", { title: row.title }),
								onClick: () => {
									unarchive(row.id).catch((reason) => {
										console.warn("session unarchive rejected:", reason);
									});
								},
								children: t("unarchive")
							})]
						}, row.id))
					}) : null
				]
			});
		}
		//#endregion
		//#region lib/types/client/locales.js
		/** Copy dictionaries for the archived-session Settings page. */
		/** Simplified Chinese dictionary and key source of truth. */
		const zh = {
			nav: "已归档会话",
			search: "搜索已归档会话",
			loading: "正在读取会话…",
			empty: "暂无已归档会话。",
			unavailable: "这里没有可恢复的已归档会话。",
			emptySearch: "没有匹配的会话。",
			unarchive: "取消归档",
			unarchiveNamed: "取消归档 {title}",
			ungrouped: "未分组",
			"time.now": "刚刚",
			"time.minutes": "{n}分钟",
			"time.hours": "{n}小时",
			"time.days": "{n}天",
			"time.months": "{n}个月",
			"time.years": "{n}年"
		};
		/** English dictionary checked against the Chinese key set. */
		const en = {
			nav: "Archived sessions",
			search: "Search archived sessions",
			loading: "Reading sessions…",
			empty: "No archived sessions.",
			unavailable: "No archived session here can be restored.",
			emptySearch: "No matching sessions.",
			unarchive: "Unarchive",
			unarchiveNamed: "Unarchive {title}",
			ungrouped: "Ungrouped",
			"time.now": "now",
			"time.minutes": "{n}min",
			"time.hours": "{n}h",
			"time.days": "{n}d",
			"time.months": "{n}mo",
			"time.years": "{n}y"
		};
		//#endregion
		//#region lib/types/client/index.js
		/** Archived-session Settings page, browser half. */
		/** Dictionary namespace owned by this plugin. */
		const NS = "settings.archivedSessions";
		/** Services required by the Settings registration and the archive write. */
		const inject = [
			"slots",
			"locale",
			"uiWorkspace"
		];
		/** Contribute the archived-session page to Settings. */
		function apply(ctx) {
			ctx.effect(() => ctx.locale.register(NS, {
				zh,
				en
			}), "ui-settings-unarchive-sessions: dictionaries");
			const t = ctx.locale.bind(NS);
			const injected = () => ({ unarchive: (sessionId) => ctx.uiWorkspace.unarchiveSession(sessionId) });
			ctx.slots.inject("settings.section", () => ctx.slots.register({
				name: "settings.section",
				id: "archived-sessions",
				order: 25,
				label: () => t("nav"),
				locale: NS,
				inject: injected
			}, ArchivedSessionsSection));
		}
		//#endregion
		exports.apply = apply;
		exports.inject = inject;
		return module.exports;
	}
});

//# sourceMappingURL=client.js.map