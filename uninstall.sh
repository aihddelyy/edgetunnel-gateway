#!/bin/sh
set -e

# ========================================================
#  edt-gateway OpenWrt 系统级一键全自动卸载脚本
#  功能: 平滑停止服务、清理自启脚本、删除二进制与日志、可选项清理配置与用户
# ========================================================

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m'

echo -e "${RED}==============================================${NC}"
echo -e "${RED}         edt-gateway 一键卸载与清理助手        ${NC}"
echo -e "${RED}==============================================${NC}"

# 1. 确认是否彻底清理配置文件
PURGE=0
if [ "$1" = "--purge" ] || [ "$1" = "-p" ]; then
    PURGE=1
elif [ -t 0 ]; then
    printf "${YELLOW}[?] 是否同时彻底删除配置文件与历史备份 (/etc/edt-gateway)？[y/N]: ${NC}"
    read -r CONFIRM
    case "$CONFIRM" in
        [yY][eE][sS]|[yY])
            PURGE=1
            ;;
        *)
            PURGE=0
            ;;
    esac
fi

# 2. 停止并禁用 Procd 服务
echo -e "${CYAN}[1/6] 正在停止运行中的网关服务并禁用开机自启...${NC}"
if [ -f "/etc/init.d/edt-gateway" ]; then
    /etc/init.d/edt-gateway stop 2>/dev/null || true
    /etc/init.d/edt-gateway disable 2>/dev/null || true
fi

# 确保所有相关进程完全退出 (兼顾 killall 与 pkill)
killall -9 edt-gateway 2>/dev/null || true
pkill -9 -f edt-gateway 2>/dev/null || true

# 3. 移除系统服务脚本与自启软链接
echo -e "${CYAN}[2/6] 清理 Procd 系统服务脚本与 rc.d 软链接...${NC}"
rm -f /etc/init.d/edt-gateway 2>/dev/null || true
rm -f /etc/rc.d/S*edt-gateway 2>/dev/null || true
rm -f /etc/rc.d/K*edt-gateway 2>/dev/null || true

# 4. 移除系统命令、软链接与固件升级保护 (兼顾早期实体程序直接存放在 /usr/bin 的旧版本)
echo -e "${CYAN}[3/6] 清理系统命令与固件保留规则...${NC}"
rm -f /usr/bin/edt-gateway /usr/bin/edt-gateway.bak 2>/dev/null || true
rm -f /lib/upgrade/keep.d/edt-gateway 2>/dev/null || true
rm -rf /tmp/edt-gateway* /var/run/edt-gateway 2>/dev/null || true

# 5. 清理运行日志目录
echo -e "${CYAN}[4/6] 清理系统运行日志 (/var/log/edt-gateway)...${NC}"
rm -rf /var/log/edt-gateway 2>/dev/null || true

# 6. 处理程序文件与配置目录
CONF_DIR="/etc/edt-gateway"
TARGET_BIN="${CONF_DIR}/edt-gateway"

if [ "$PURGE" -eq 1 ]; then
    echo -e "${CYAN}[5/6] 正在彻底清除配置目录与所有历史备份 (${CONF_DIR})...${NC}"
    rm -rf "$CONF_DIR" 2>/dev/null || true
else
    echo -e "${CYAN}[5/6] 正在清理程序二进制文件，保留配置文件与历史备份...${NC}"
    rm -f "${CONF_DIR}/edt-gateway"* 2>/dev/null || true
    echo -e "${GREEN}[✔] 配置文件已保留在: ${YELLOW}${CONF_DIR}/config.json${NC}"
fi

# 7. 可选清理专属用户 edtgw
echo -e "${CYAN}[6/6] 检查专属系统用户 edtgw...${NC}"
if grep -q "^edtgw:" /etc/passwd 2>/dev/null; then
    sed -i '/^edtgw:/d' /etc/passwd 2>/dev/null || true
    sed -i '/^edtgw:/d' /etc/group 2>/dev/null || true
    echo -e "${GREEN}[✔] 已安全移除系统专属用户与用户组 edtgw${NC}"
fi

echo -e "\n${GREEN}==============================================${NC}"
echo -e "${GREEN}      🎉 edt-gateway 已成功从系统完全卸载！    ${NC}"
if [ "$PURGE" -eq 0 ] && [ -d "$CONF_DIR" ]; then
    echo -e "💡 提示: 若日后重新安装，原配置文件 (${YELLOW}${CONF_DIR}/config.json${NC}) 将自动生效。"
fi
echo -e "${GREEN}==============================================${NC}"
