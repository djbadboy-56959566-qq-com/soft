-- DjMihomo LuCI 控制器
-- 兼容性: 经典 Lua controller 注册菜单, 覆盖所有 OpenWrt LuCI 版本
-- (15.05 / 17.01 / 18.06 / 19.07 / 21.02 / 22.03 / 23.05 / 24.10 / SNAPSHOT,
--  以及 ImmortalWrt 各版本)。不依赖 menu.d JSON, 不依赖 JS view, 不用任何
-- PonWrt / ImmortalWrt 专有 API, 只用标准 LuCI (luci-base) 的 API。
-- 菜单: 服务 -> DjMihomo
-- 功能: 状态页 + 启动/停止/重启 (调 /etc/init.d/djmihomo)

module("luci.controller.djmihomo", package.seeall)

function index()
	entry({"admin", "services", "djmihomo"},
		template("djmihomo/status"), _("DjMihomo"), 60)
	entry({"admin", "services", "djmihomo", "start"},
		call("action_start"), nil, nil).leaf = true
	entry({"admin", "services", "djmihomo", "stop"},
		call("action_stop"), nil, nil).leaf = true
	entry({"admin", "services", "djmihomo", "restart"},
		call("action_restart"), nil, nil).leaf = true
end

function action_start()
	luci.sys.call("/etc/init.d/djmihomo start >/dev/null 2>&1")
	luci.sys.call("sleep 2")
	luci.http.redirect(luci.dispatcher.build_url("admin", "services", "djmihomo"))
end

function action_stop()
	luci.sys.call("/etc/init.d/djmihomo stop >/dev/null 2>&1")
	luci.sys.call("sleep 1")
	luci.http.redirect(luci.dispatcher.build_url("admin", "services", "djmihomo"))
end

function action_restart()
	luci.sys.call("/etc/init.d/djmihomo restart >/dev/null 2>&1")
	luci.sys.call("sleep 2")
	luci.http.redirect(luci.dispatcher.build_url("admin", "services", "djmihomo"))
end
