let isPausedLog = false;
    let isNeedSetup = false;
    let eventSourceInstance = null;
    let statusInterval = null;

    function showConfirm(message, options = {}) {
      return new Promise((resolve) => {
        const modal = document.getElementById('custom-confirm-modal');
        if (!modal) {
          resolve(window.confirm(message));
          return;
        }
        const titleEl = document.getElementById('custom-confirm-title');
        const msgEl = document.getElementById('custom-confirm-msg');
        const iconEl = document.getElementById('custom-confirm-icon');
        const okBtn = document.getElementById('custom-confirm-ok');
        const cancelBtn = document.getElementById('custom-confirm-cancel');

        titleEl.innerText = options.title || '操作确认';
        msgEl.innerHTML = escapeHtml(message).replace(/\n/g, '<br>');
        okBtn.innerText = options.confirmText || '确定';
        cancelBtn.innerText = options.cancelText || '取消';

        if (options.isDanger) {
          okBtn.className = 'btn btn-danger';
          iconEl.innerText = options.icon || '🗑️';
          iconEl.style.background = 'rgba(239, 68, 68, 0.15)';
          iconEl.style.borderColor = 'rgba(239, 68, 68, 0.3)';
          iconEl.style.color = '#ef4444';
        } else if (options.isWarning) {
          okBtn.className = 'btn btn-primary';
          iconEl.innerText = options.icon || '⚠️';
          iconEl.style.background = 'rgba(245, 158, 11, 0.15)';
          iconEl.style.borderColor = 'rgba(245, 158, 11, 0.3)';
          iconEl.style.color = '#f59e0b';
        } else {
          okBtn.className = 'btn btn-primary';
          iconEl.innerText = options.icon || '⚡';
          iconEl.style.background = 'rgba(99, 102, 241, 0.15)';
          iconEl.style.borderColor = 'rgba(99, 102, 241, 0.3)';
          iconEl.style.color = '#818cf8';
        }

        modal.style.display = 'flex';

        const onKey = (e) => {
          if (e.key === 'Escape') cleanup(false);
          if (e.key === 'Enter') cleanup(true);
        };

        const cleanup = (res) => {
          window.removeEventListener('keydown', onKey);
          modal.style.display = 'none';
          okBtn.onclick = null;
          cancelBtn.onclick = null;
          resolve(res);
        };

        okBtn.onclick = () => cleanup(true);
        cancelBtn.onclick = () => cleanup(false);
        window.addEventListener('keydown', onKey);
      });
    }

    function showToast(msg, isErr = false) {
      const t = document.getElementById('toast');
      if (!t) return;
      t.innerText = msg;
      t.style.borderColor = isErr ? 'rgba(239, 68, 68, 0.5)' : 'rgba(16, 185, 129, 0.5)';
      t.style.display = 'block';
      setTimeout(() => { t.style.display = 'none'; }, 3000);
    }

    function switchTab(tabId) {
      document.querySelectorAll('.tab-pane').forEach(el => el.classList.remove('active'));
      document.querySelectorAll('.tab-btn').forEach(el => el.classList.remove('active'));
      const targetPane = document.getElementById('tab-' + tabId);
      if (targetPane) targetPane.classList.add('active');
      const activeBtn = document.querySelector('.tab-btn[onclick*="' + tabId + '"]');
      if (activeBtn) {
        activeBtn.classList.add('active');
        try {
          activeBtn.scrollIntoView({ behavior: 'smooth', block: 'nearest', inline: 'center' });
        } catch {}
      }
      if (tabId === 'cfusage') {
        loadCfAccounts();
      }
      if (tabId === 'logs') {
        if (!eventSourceInstance || eventSourceInstance.readyState === EventSource.CLOSED) {
          initLogStream();
        }
      }
    }

    async function authFetch(url, options = {}) {
      options.headers = options.headers || {};
      const token = localStorage.getItem('edgetunnel_token');
      if (token) {
        options.headers['Authorization'] = 'Bearer ' + token;
      }
      const res = await fetch(url, options);
      if (res.status === 401) {
        localStorage.removeItem('edgetunnel_token');
        applyAuthState(false);
        throw new Error('未授权或登录已过期');
      }
      return res;
    }

    function getSavedCredentials() {
      try {
        const raw = localStorage.getItem('edgetunnel_saved_creds');
        if (!raw) return null;
        const parsed = JSON.parse(raw);
        let pwd = '';
        if (parsed.password) {
          try {
            pwd = decodeURIComponent(escape(atob(parsed.password)));
          } catch {
            try { pwd = atob(parsed.password); } catch { pwd = parsed.password; }
          }
        }
        return { username: parsed.username || 'admin', password: pwd };
      } catch {
        return null;
      }
    }

    function saveCredentials(username, password, remember) {
      if (remember) {
        try {
          const encodedPwd = btoa(unescape(encodeURIComponent(password)));
          localStorage.setItem('edgetunnel_saved_creds', JSON.stringify({
            username,
            password: encodedPwd
          }));
        } catch {
          try {
            localStorage.setItem('edgetunnel_saved_creds', JSON.stringify({
              username,
              password
            }));
          } catch {}
        }
      } else {
        localStorage.removeItem('edgetunnel_saved_creds');
      }
    }

    function showAuthModal(needSetup) {
      isNeedSetup = needSetup;
      const modal = document.getElementById('auth-modal');
      const title = document.getElementById('auth-title');
      const subtitle = document.getElementById('auth-subtitle');
      const icon = document.getElementById('auth-icon');
      const confirmGroup = document.getElementById('auth-confirm-group');
      const submitBtn = document.getElementById('auth-submit-btn');
      const errBox = document.getElementById('auth-error');
      const userEl = document.getElementById('auth-user');
      const pwdEl = document.getElementById('auth-pwd');
      const pwdConfirmEl = document.getElementById('auth-pwd-confirm');
      const rememberCheckbox = document.getElementById('auth-remember');
      
      if (errBox) errBox.style.display = 'none';

      const savedCreds = getSavedCredentials();
      if (savedCreds) {
        if (userEl && savedCreds.username) userEl.value = savedCreds.username;
        if (pwdEl && savedCreds.password) pwdEl.value = savedCreds.password;
        if (rememberCheckbox) rememberCheckbox.checked = true;
      } else {
        if (pwdEl) pwdEl.value = '';
        if (rememberCheckbox) rememberCheckbox.checked = true;
      }
      if (pwdConfirmEl) pwdConfirmEl.value = '';

      if (needSetup) {
        if (icon) icon.innerText = '🚀';
        if (title) title.innerText = '首次启动 - 设置管理员密码';
        if (subtitle) subtitle.innerText = '欢迎使用 EdgeTunnel LB 分流网关！请先为控制台初始化账号与密码以确保安全。';
        if (confirmGroup) confirmGroup.style.display = 'block';
        if (submitBtn) submitBtn.innerText = '完成设置并进入控制台';
      } else {
        if (icon) icon.innerText = '🔐';
        if (title) title.innerText = '管理员登录';
        if (subtitle) subtitle.innerText = '请输入用户名与密码以访问管理控制台';
        if (confirmGroup) confirmGroup.style.display = 'none';
        if (submitBtn) submitBtn.innerText = '登 录';
      }
      if (modal) modal.style.display = 'flex';
      setTimeout(() => {
        if (pwdEl) {
          pwdEl.focus();
          if (pwdEl.value) pwdEl.select();
        }
      }, 100);
    }

    function hideAuthModal(username) {
      const modal = document.getElementById('auth-modal');
      if (modal) modal.style.display = 'none';
      const badge = document.getElementById('user-badge');
      if (badge) badge.style.display = 'flex';
      const nameEl = document.getElementById('current-username');
      if (nameEl) nameEl.innerText = username || 'admin';
      const pwdUser = document.getElementById('pwd-user');
      if (pwdUser) pwdUser.value = username || 'admin';
      updateAuthBtnState();
    }

    async function submitAuthForm() {
      const userEl = document.getElementById('auth-user');
      const pwdEl = document.getElementById('auth-pwd');
      const pwdConfirmEl = document.getElementById('auth-pwd-confirm');
      const rememberCheckbox = document.getElementById('auth-remember');
      const errBox = document.getElementById('auth-error');
      if (errBox) errBox.style.display = 'none';

      const username = userEl ? userEl.value.trim() : 'admin';
      const pwd = pwdEl ? pwdEl.value : '';
      const pwdConfirm = pwdConfirmEl ? pwdConfirmEl.value : '';
      const shouldRemember = rememberCheckbox ? rememberCheckbox.checked : true;

      if (!username) {
        if (errBox) { errBox.innerText = '请输入管理员账号'; errBox.style.display = 'block'; }
        return;
      }
      if (!pwd || pwd.length < 4) {
        if (errBox) { errBox.innerText = '密码长度至少需 4 位'; errBox.style.display = 'block'; }
        return;
      }

      if (isNeedSetup) {
        if (pwd !== pwdConfirm) {
          if (errBox) { errBox.innerText = '两次输入的密码不一致'; errBox.style.display = 'block'; }
          return;
        }
        try {
          const res = await fetch('/api/auth/setup', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username, password: pwd })
          });
          const data = await res.json();
          if (data.ok) {
            localStorage.setItem('edgetunnel_token', data.token);
            saveCredentials(username, pwd, shouldRemember);
            updateAuthBtnState();
            hideAuthModal(data.username);
            showToast('🎉 管理员账号与密码初始化成功！');
            startApp();
          } else {
            if (errBox) { errBox.innerText = data.error || '设置失败'; errBox.style.display = 'block'; }
          }
        } catch (e) {
          if (errBox) { errBox.innerText = '网络请求失败: ' + e.message; errBox.style.display = 'block'; }
        }
      } else {
        try {
          const res = await fetch('/api/auth/login', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ username, password: pwd })
          });
          const data = await res.json();
          if (data.ok) {
            localStorage.setItem('edgetunnel_token', data.token);
            saveCredentials(username, pwd, shouldRemember);
            hideAuthModal(data.username);
            showToast('✅ 登录成功');
            startApp();
          } else {
            if (errBox) { errBox.innerText = data.error || '用户名或密码错误'; errBox.style.display = 'block'; }
          }
        } catch (e) {
          if (errBox) { errBox.innerText = '登录失败: ' + e.message; errBox.style.display = 'block'; }
        }
      }
    }

        // ================= 认证状态与按钮管理 =================
    // ================= 认证状态与访客/管理员视图切换 =================
    function applyAuthState(isLoggedIn) {
      // 1. 控制除监控中心以外的配置选项卡显示
      document.querySelectorAll('.admin-tab').forEach(el => {
        el.style.display = isLoggedIn ? 'inline-block' : 'none';
      });

      // 2. 控制顶栏重启网关与重置熔断按钮显示
      document.querySelectorAll('.admin-btn').forEach(el => {
        el.style.display = isLoggedIn ? 'inline-block' : 'none';
      });

      // 3. 右上角按钮切换
      const authBtn = document.getElementById('auth-action-btn');
      if (authBtn) {
        if (isLoggedIn) {
          authBtn.innerHTML = '🚪 <span class="btn-text-full">退出登录</span><span class="btn-text-short">退出</span>';
          authBtn.title = '退出当前管理员账号';
          authBtn.className = 'btn btn-outline btn-sm';
        } else {
          authBtn.innerHTML = '🔑 <span class="btn-text-full">登录</span><span class="btn-text-short">登录</span>';
          authBtn.title = '登录管理员账号以解锁全部配置';
          authBtn.className = 'btn btn-primary btn-sm';
        }
      }

      // 4. 若退出登录且不在监控中心，自动平滑切回监控大盘
      if (!isLoggedIn) {
        switchTab('dashboard');
      }
    }

    function updateAuthBtnState() {
      const token = localStorage.getItem('edgetunnel_token');
      applyAuthState(!!token);
    }

    function handleAuthAction() {
      const token = localStorage.getItem('edgetunnel_token');
      if (token) {
        triggerLogout();
      } else {
        showAuthModal(false);
      }
    }

    async function triggerLogout() {
      try {
        await authFetch('/api/auth/logout', { method: 'POST' });
      } catch {}
      localStorage.removeItem('edgetunnel_token');
      applyAuthState(false);
      showToast('已退出登录，当前为只读监控大盘模式');
    }

    async function submitChangePassword() {
      const oldPassword = document.getElementById('pwd-old').value;
      const newPassword = document.getElementById('pwd-new').value;
      const confirmPwd = document.getElementById('pwd-confirm').value;

      if (!oldPassword) {
        showToast('请输入当前旧密码', true);
        return;
      }
      if (!newPassword || newPassword.length < 4) {
        showToast('新密码长度至少需 4 位', true);
        return;
      }
      if (newPassword !== confirmPwd) {
        showToast('两次新密码输入不一致', true);
        return;
      }

      try {
        const res = await authFetch('/api/auth/change-password', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ oldPassword, newPassword })
        });
        const data = await res.json();
        if (data.ok) {
          showToast('✅ 管理员密码更新成功！');
          document.getElementById('pwd-old').value = '';
          document.getElementById('pwd-new').value = '';
          document.getElementById('pwd-confirm').value = '';
        } else {
          showToast('❌ ' + data.error, true);
        }
      } catch (e) {
        showToast('❌ 修改失败: ' + e.message, true);
      }
    }

    function formatDuration(totalSeconds) {
      const sec = Math.max(0, Math.floor(totalSeconds || 0));
      const totalMinutes = Math.floor(sec / 60);

      if (totalMinutes < 60) {
        return `${totalMinutes} 分钟`;
      }

      const days = Math.floor(totalMinutes / (24 * 60));
      const hours = Math.floor((totalMinutes % (24 * 60)) / 60);
      const minutes = totalMinutes % 60;

      if (days >= 1) {
        return `${days} 天 ${hours} 小时 ${minutes} 分钟`;
      } else {
        return `${hours} 小时 ${minutes} 分钟`;
      }
    }

    async function loadStatus() {
      try {
        const res = await authFetch('/api/status');
        const data = await res.json();
        const statWs = document.getElementById('stat-ws');
        const statXhttp = document.getElementById('stat-xhttp');
        const statCircuit = document.getElementById('stat-circuit');
        const statUptime = document.getElementById('stat-uptime');
        const statMemory = document.getElementById('stat-memory');

        if (statWs) statWs.innerText = data.metrics.wsRequests;
        if (statXhttp) statXhttp.innerText = data.metrics.xhttpRequests;
        if (statCircuit) statCircuit.innerText = data.metrics.circuitTrips;
        if (statUptime) statUptime.innerText = formatDuration(data.uptime);
        
        const statMemoryText = document.getElementById('stat-memory-text');
        const statMemoryPill = document.getElementById('stat-memory-pill');
        const memMB = parseFloat(data.memoryMB) || 0;

        if (statMemoryText) {
          statMemoryText.innerText = '内存占用: ' + memMB.toFixed(1) + ' MB';
        } else if (statMemory) {
          statMemory.innerText = '内存占用: ' + memMB.toFixed(1) + ' MB';
        }

        if (statMemoryPill) {
          if (memMB < 100) {
            statMemoryPill.innerText = '极佳';
            statMemoryPill.style.background = 'rgba(16, 185, 129, 0.15)';
            statMemoryPill.style.color = '#34d399';
          } else if (memMB < 200) {
            statMemoryPill.innerText = '正常';
            statMemoryPill.style.background = 'rgba(59, 130, 246, 0.15)';
            statMemoryPill.style.color = '#60a5fa';
          } else {
            statMemoryPill.innerText = '偏高';
            statMemoryPill.style.background = 'rgba(239, 68, 68, 0.15)';
            statMemoryPill.style.color = '#f87171';
          }
        }

        // 优先更新 CF 额度账号缓存，保障后端卡片直接读取最新联动数据
        if (data.cfAccountsBrief && data.cfAccountsBrief.length > 0) {
          availableCfAccounts = data.cfAccountsBrief;
          cfAccountsData = data.cfAccountsBrief;
          renderBackendAccountMappingForm();
        }

        // 更新 CF 额度自动巡检状态指示器
        const cfStatusPill = document.getElementById('cf-patrol-status-pill');
        if (cfStatusPill) {
          if (data.cfQuotaLastUpdated) {
            cfStatusPill.innerHTML = `☁️ 额度更新 ${data.cfQuotaLastUpdated}`;
            cfStatusPill.title = `后台每 ${data.cfQuotaIntervalMinutes || 5} 分钟自动静默拉取 Cloudflare 最新用量，上次同步时间: ${data.cfQuotaLastUpdated}`;
          } else {
            cfStatusPill.innerHTML = `☁️ 额度更新中...`;
          }
        }

        // 实时渲染流量迷你走势波形图
        updateTrafficSparklines(data.trafficHistory, data.metrics.xhttpRequests, data.metrics.wsRequests);

        renderBackends('worker-backends-list', data.config.workerBackends, data.health, data);
        renderBackends('pages-backends-list', data.config.pagesBackends, data.health, data);
        renderPreferNodes(data.preferNodes || []);
      } catch (e) {}
    }

    let isPingingPrefers = false;

    function renderPreferNodes(nodes) {
      const container = document.getElementById('prefer-nodes-list');
      const badge = document.getElementById('prefer-count-badge');
      if (!container) return;

      if (!nodes || nodes.length === 0) {
        if (badge) badge.innerText = '0 个节点';
        container.innerHTML = '<div style="color: var(--text-muted); font-size: 0.85rem; padding: 0.5rem 0;">暂无活跃优选节点记录（当客户端连入网关时将自动收集感知）</div>';
        return;
      }

      if (badge) badge.innerText = `${nodes.length} 个节点`;

      container.innerHTML = nodes.map(n => {
        const isDefault = !!n.isDefault;
        const targetStr = escapeHtml(n.target);

        let latencyBadge = '';
        if (n.lastLatency !== null && !isNaN(n.lastLatency)) {
          const ms = n.lastLatency;
          if (ms < 80) {
            latencyBadge = `<span class="badge badge-success" style="font-family:monospace; font-weight:700;">🟢 ${ms} ms</span>`;
          } else if (ms < 180) {
            latencyBadge = `<span class="badge" style="background:rgba(245,158,11,0.15); color:#fbbf24; border:1px solid rgba(245,158,11,0.3); font-family:monospace; font-weight:700;">🟡 ${ms} ms</span>`;
          } else {
            latencyBadge = `<span class="badge badge-danger" style="font-family:monospace; font-weight:700;">🟠 ${ms} ms</span>`;
          }
        } else if (n.lastStatus === 'timeout') {
          latencyBadge = `<span class="badge badge-danger" title="${escapeHtml(n.lastError || '超时')}">🔴 超时</span>`;
        } else if (n.lastStatus === 'error') {
          latencyBadge = `<span class="badge badge-danger" title="${escapeHtml(n.lastError || '错误')}">🔴 连接失败</span>`;
        } else {
          latencyBadge = `<span class="badge" style="background:rgba(255,255,255,0.06); color:var(--text-muted); border:1px solid var(--card-border);">⚪ 待测速</span>`;
        }

        const roleTag = isDefault
          ? `<span class="badge badge-success" style="font-size:0.7rem; padding:0.1rem 0.45rem;">🌟 默认兜底</span>`
          : `<span class="badge" style="background:rgba(59,130,246,0.12); color:#60a5fa; border:1px solid rgba(59,130,246,0.25); font-size:0.7rem; padding:0.1rem 0.45rem;">⚡ 客户端携带</span>`;

        const diffSec = Math.max(0, Math.floor((Date.now() - n.lastSeen) / 1000));
        let activeTimeStr = '刚刚';
        if (diffSec >= 60) {
          activeTimeStr = formatDuration(diffSec) + '前';
        } else if (diffSec > 5) {
          activeTimeStr = `${diffSec} 秒前`;
        }

        const clientsStr = (n.clients && n.clients.length > 0)
          ? n.clients.map(c => `<code style="font-size:0.72rem; background:rgba(255,255,255,0.05); padding:1px 4px; border-radius:3px;">${escapeHtml(c)}</code>`).join(' ')
          : '<span style="color:var(--text-muted);">网关自身</span>';

        const reqCount = (n.requestCount || 0).toLocaleString();

        const btnDefault = isDefault
          ? `<span style="font-size:0.75rem; color:var(--text-muted); padding:0.2rem 0.4rem;">[当前兜底]</span>`
          : `<button class="btn btn-outline btn-sm admin-btn" style="font-size:0.72rem; padding:0.15rem 0.45rem;" onclick="setAsDefaultPrefer('${escapeHtml(n.target)}')" title="将此节点设为网关全局默认兜底">🌟 设为兜底</button>`;

        return `
          <div class="backend-card" style="padding: 0.85rem 1.05rem;">
            <div style="display: flex; justify-content: space-between; align-items: flex-start; gap: 0.5rem;">
              <div style="flex: 1; min-width: 0;">
                <div style="display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap;">
                  <span class="backend-name" style="font-size: 0.92rem; color: #60a5fa; cursor: pointer;" onclick="copyText('${escapeHtml(n.target)}')" title="点击复制">${targetStr}</span>
                  ${roleTag}
                </div>
                <div style="margin-top: 0.4rem; font-size: 0.74rem; color: var(--text-muted); display: flex; flex-direction: column; gap: 0.2rem;">
                  <div>📊 请求量: <strong style="color:var(--text);">${reqCount}</strong> 次 · 最近活跃: ${activeTimeStr}</div>
                  <div>💻 来源设备: ${clientsStr}</div>
                </div>
              </div>
              <div style="flex-shrink: 0; text-align: right;">
                ${latencyBadge}
              </div>
            </div>
            <div style="display: flex; justify-content: flex-end; align-items: center; gap: 0.4rem; margin-top: 0.4rem; padding-top: 0.4rem; border-top: 1px solid rgba(255,255,255,0.04);">
              <button class="btn btn-outline btn-sm" style="font-size:0.72rem; padding:0.15rem 0.5rem;" onclick="pingSinglePreferNode('${escapeHtml(n.target)}')">⚡ 测速</button>
              ${btnDefault}
            </div>
          </div>
        `;
      }).join('');
    }

    async function pingAllPreferNodes() {
      if (isPingingPrefers) return;
      isPingingPrefers = true;
      const btn = document.getElementById('btn-ping-prefer');
      const origText = btn ? btn.innerHTML : '⚡ 一键测速';
      if (btn) {
        btn.innerHTML = '⏳ 测速中...';
        btn.disabled = true;
      }
      try {
        const res = await authFetch('/api/prefer/ping');
        const d = await res.json();
        if (d.ok && d.nodes) {
          renderPreferNodes(d.nodes);
        }
      } catch (err) {
        alert('测速失败: ' + err.message);
      } finally {
        isPingingPrefers = false;
        if (btn) {
          btn.innerHTML = origText;
          btn.disabled = false;
        }
      }
    }

    function pingBackends() {
      pingAllPreferNodes();
    }

    async function pingSinglePreferNode(target) {
      try {
        const res = await authFetch('/api/prefer/ping?target=' + encodeURIComponent(target));
        const d = await res.json();
        if (d.ok && d.nodes) {
          renderPreferNodes(d.nodes);
        }
      } catch (err) {
        alert('测速异常: ' + err.message);
      }
    }

    async function setAsDefaultPrefer(target) {
      const ok = await showConfirm(`确定将【${target}】设置为网关全局默认兜底优选服务器吗？\n\n设置后将持久化保存到 config.json。`, {
        title: '设为默认优选服务器',
        confirmText: '设为默认',
        icon: '🌟'
      });
      if (!ok) return;
      try {
        const res = await authFetch('/api/prefer/set-default', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ target })
        });
        const d = await res.json();
        if (d.ok) {
          showToast(`✅ 已成功将【${target}】设为全局默认兜底服务器！`);
          if (d.nodes) renderPreferNodes(d.nodes);
          const el = document.getElementById('cfg-defaultPrefer');
          if (el) el.value = target;
        } else {
          showToast('设置失败: ' + (d.error || '未知错误'), true);
        }
      } catch (err) {
        showToast('请求失败: ' + err.message, true);
      }
    }

    async function clearPreferNodes() {
      const ok = await showConfirm('确定要清除非默认的历史优选节点记录吗？\n\n清理后仅保留当前系统默认兜底优选节点。', {
        title: '清理历史优选节点',
        confirmText: '立即清理',
        icon: '🧹',
        isDanger: true
      });
      if (!ok) return;
      try {
        const res = await authFetch('/api/prefer/clear', { method: 'POST' });
        const d = await res.json();
        if (d.ok && d.nodes) {
          showToast('✅ 历史优选节点记录已清理');
          renderPreferNodes(d.nodes);
        }
      } catch (err) {
        showToast('操作失败: ' + err.message, true);
      }
    }

    // HTTP 局域网访问属于非安全上下文，navigator.clipboard 为 undefined，必须回退 execCommand
    function copyToClipboard(text) {
      if (window.isSecureContext && navigator.clipboard && navigator.clipboard.writeText) {
        return navigator.clipboard.writeText(text);
      }
      const ta = document.createElement('textarea');
      ta.value = text;
      ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0';
      document.body.appendChild(ta);
      ta.select();
      let ok = false;
      try { ok = document.execCommand('copy'); } catch (e) { ok = false; }
      ta.remove();
      return ok ? Promise.resolve() : Promise.reject(new Error('execCommand copy failed'));
    }

    function copyText(txt) {
      copyToClipboard(txt).then(() => {
        showToast('📋 已复制优选节点地址: ' + txt);
      }).catch(() => {
        prompt('请手动复制:', txt);
      });
    }

    async function toggleBackendDomain(domain, disabled) {
      const actionText = disabled ? '暂停分流' : '恢复分流';
      const ok = await showConfirm(`确定要【${actionText}】后端节点【${domain}】吗？\n\n${disabled ? '暂停后该节点将立即停止接收新流量并从活跃轮询池中移出。' : '恢复后该节点将重新加入负载均衡活跃轮询池。'}`, {
        title: actionText + '确认',
        confirmText: actionText,
        icon: disabled ? '⏸️' : '▶️',
        isWarning: disabled
      });
      if (!ok) return;
      try {
        const res = await authFetch('/api/backend/toggle', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ backend: domain, disabled })
        });
        const data = await res.json();
        if (data.ok) {
          showToast(`✅ 节点【${domain}】${actionText}成功！`);
          loadStatus();
        } else {
          showToast('操作失败: ' + (data.error || '未知错误'), true);
        }
      } catch (err) {
        showToast('请求出错: ' + err.message, true);
      }
    }

    function renderBackends(elemId, list, healthMap, statusData) {
      const el = document.getElementById(elemId);
      if (!el) return;
      if (!list || list.length === 0) {
        el.innerHTML = '<div style="color: var(--text-muted);">未配置后端</div>';
        return;
      }
      const isWorker = elemId.includes('worker');
      const prefix = isWorker ? '🔶 ' : '⚡️ ';
      const map = statusData?.config?.backendAccountMap || {};
      const displayMode = statusData?.config?.quotaDisplayMode || 'protocol';
      const briefAccounts = statusData?.cfAccountsBrief || cfAccountsData || availableCfAccounts || [];
      const disabledList = statusData?.disabledBackends || statusData?.config?.disabledBackends || [];
      const statsMap = statusData?.backendStats || {};

      el.innerHTML = list.map(b => {
        const cleanB = String(b).trim();
        const isDisabled = disabledList.includes(cleanB) || disabledList.includes(b);
        const h = healthMap ? (healthMap[cleanB] || healthMap[b]) : null;
        const remainSeconds = h ? Math.ceil((h.blockUntil - Date.now()) / 1000) : 0;
        const isHealthy = !h || h.isHealthy || remainSeconds <= 0;

        let badge = '';
        if (isDisabled) {
          badge = '<span class="badge" style="background: rgba(245, 158, 11, 0.15); color: #f59e0b; border: 1px solid rgba(245, 158, 11, 0.3);">⏸️ 已手动暂停</span>';
        } else if (isHealthy) {
          badge = '<span class="badge badge-success">🟢 正常运行</span>';
        } else {
          badge = '<span class="badge badge-danger">🔴 熔断隔离中 (' + remainSeconds + 's)</span>';
        }

        // 查找绑定的 CF 账户 (多重容错查找)
        const boundAccId = map[cleanB] || map[b];
        let boundAcc = null;
        if (boundAccId) {
          boundAcc = briefAccounts.find(a => (
            String(a.id) === String(boundAccId) || 
            String(a.accountId) === String(boundAccId) || 
            String(a.name) === String(boundAccId)
          ));
        }

        // 单节点专属流量统计
        const bStat = statsMap[cleanB] || statsMap[b];
        const totalReq = bStat ? (bStat.total || 0) : 0;
        const successRate = bStat ? (bStat.successRate !== undefined ? bStat.successRate : (totalReq > 0 ? (bStat.success / totalReq * 100).toFixed(1) : 100)) : 100;
        const statsBadgeHtml = '<span style="font-size: 0.72rem; color: var(--text-muted); background: rgba(255,255,255,0.04); border: 1px solid var(--card-border); padding: 0.1rem 0.45rem; border-radius: 4px; font-family: monospace;">' +
          '📊 转发: ' + totalReq.toLocaleString() + ' 次 · 成功率 ' + successRate + '%' +
        '</span>';

        let quotaHtml = '';
        let accTagHtml = '<span style="font-size: 0.72rem; color: var(--text-muted); opacity: 0.85;">⚪ 未关联 CF 账户</span>';

        if (boundAcc) {
          accTagHtml = '<span style="background: rgba(99, 102, 241, 0.12); color: #818cf8; border: 1px solid rgba(99, 102, 241, 0.25); border-radius: 4px; padding: 0.1rem 0.45rem; font-size: 0.72rem; font-weight: 500;">👤 ' + escapeHtml(boundAcc.name) + '</span>';

          let used = 0;
          let limit = 50000;
          let metricLabel = '';

          if (displayMode === 'total') {
            used = boundAcc.total || ((boundAcc.workers || 0) + (boundAcc.pages || 0));
            limit = boundAcc.max || 100000;
            metricLabel = '📊 账户总消耗: ' + used.toLocaleString() + ' / ' + limit.toLocaleString();
          } else {
            limit = 50000;
            if (isWorker) {
              used = boundAcc.workers || 0;
              metricLabel = '🔶 Worker: ' + used.toLocaleString() + ' / ' + limit.toLocaleString();
            } else {
              used = boundAcc.pages || 0;
              metricLabel = '⚡️ Pages: ' + used.toLocaleString() + ' / ' + limit.toLocaleString();
            }
          }

          const pct = limit > 0 ? (used / limit * 100) : 0;
          let pctColor = '#10b981';
          let barGrad = '#10b981';
          if (pct >= 80) {
            pctColor = '#ef4444';
            barGrad = '#ef4444';
          } else if (pct >= 50) {
            pctColor = '#f59e0b';
            barGrad = '#f59e0b';
          }

          quotaHtml = 
            '<div style="margin-top: 0.25rem;">' +
              '<div style="display: flex; justify-content: space-between; align-items: baseline; font-size: 0.74rem; margin-bottom: 3px;">' +
                '<span style="color: var(--text-muted); font-family: monospace;">' + metricLabel + '</span>' +
                '<span style="color: ' + pctColor + '; font-weight: 700; font-family: monospace;">' + pct.toFixed(1) + '%</span>' +
              '</div>' +
              '<div style="height: 4px; background: rgba(255, 255, 255, 0.08); border-radius: 999px; overflow: hidden;">' +
                '<div style="height: 100%; width: ' + Math.min(100, Math.max(2, pct)) + '%; background: ' + barGrad + '; border-radius: 999px; transition: width 0.3s ease;"></div>' +
              '</div>' +
            '</div>';
        } else if (boundAccId) {
          accTagHtml = '<span style="background: rgba(99, 102, 241, 0.12); color: #818cf8; border: 1px solid rgba(99, 102, 241, 0.25); border-radius: 4px; padding: 0.1rem 0.45rem; font-size: 0.72rem; font-weight: 500;">👤 ' + escapeHtml(boundAccId) + '</span>';
        }

        const toggleHtml = 
          '<div class="switch-toggle ' + (!isDisabled ? 'checked' : '') + '" ' +
               'onclick="toggleBackendDomain(\'' + escapeHtml(cleanB) + '\', ' + (!isDisabled) + ')" ' +
               'title="' + (!isDisabled ? '分流中（点击暂停）' : '已暂停（点击开启）') + '">' +
            '<div class="switch-thumb"></div>' +
          '</div>';

        const cardStyle = isDisabled ? 'opacity: 0.65; border-color: rgba(245, 158, 11, 0.25);' : '';

        return '<div class="backend-card" style="' + cardStyle + '">' +
                 '<div style="display: flex; justify-content: space-between; align-items: flex-start; gap: 0.5rem;">' +
                   '<div style="flex: 1; min-width: 0;">' +
                     '<div class="backend-name" style="word-break: break-all;">' + prefix + b + '</div>' +
                     '<div style="margin-top: 4px; display: flex; align-items: center; gap: 0.4rem; flex-wrap: wrap;">' + 
                       accTagHtml + 
                       statsBadgeHtml +
                     '</div>' +
                     (h && !h.isHealthy && remainSeconds > 0 && !isDisabled ? '<div style="font-size:0.75rem; color:#f87171; margin-top:3px;">' + h.reason + '</div>' : '') +
                   '</div>' +
                   '<div style="flex-shrink: 0; display: flex; align-items: center; gap: 0.55rem;">' + 
                     badge + 
                     toggleHtml +
                   '</div>' +
                 '</div>' +
                 quotaHtml +
               '</div>';
      }).join('');
    }

    // ================= 功能 2: 历史配置快照与一键回滚 =================
    async function loadConfigBackups() {
      const container = document.getElementById('config-backups-container');
      if (!container) return;
      try {
        const res = await authFetch('/api/config/backups');
        const data = await res.json();
        if (!data.ok || !data.backups || data.backups.length === 0) {
          container.innerHTML = '<div style="color: var(--text-muted); font-size: 0.85rem; text-align: center; padding: 1rem 0;">暂无历史配置快照（每次点击【保存配置】时将自动生成）</div>';
          return;
        }

        container.innerHTML = data.backups.map(b => {
          return '<div style="display: flex; justify-content: space-between; align-items: center; padding: 0.85rem 1rem; border: 1px solid var(--card-border); border-radius: 10px; margin-bottom: 0.75rem; background: rgba(255,255,255,0.02); flex-wrap: wrap; gap: 0.5rem;">' +
            '<div>' +
              '<div style="font-weight: 700; font-size: 0.95rem; color: #fff; display: flex; align-items: center; gap: 0.5rem;">' +
                '<span>🕒 ' + b.time + '</span>' +
              '</div>' +
              '<div style="font-size: 0.78rem; color: var(--text-muted); margin-top: 0.25rem; font-family: monospace;">' +
                '兜底: ' + b.defaultPrefer + ' | Worker: ' + b.workersCount + ' 个 | Pages: ' + b.pagesCount + ' 个 | 采样: ' + b.sampleMinutes + 'm' +
              '</div>' +
            '</div>' +
            '<button class="btn btn-outline btn-sm" onclick="rollbackConfig(\'' + b.id + '\')" style="font-size: 0.8rem; padding: 0.35rem 0.75rem; border-color: rgba(239, 68, 68, 0.4); color: #fca5a5;">' +
              '↺ 恢复此快照' +
            '</button>' +
          '</div>';
        }).join('');
      } catch (err) {
        container.innerHTML = '<div style="color: #ef4444; font-size: 0.85rem; text-align: center; padding: 1rem 0;">加载快照失败: ' + err.message + '</div>';
      }
    }

    async function rollbackConfig(id) {
      const ok = await showConfirm('确定要将网关配置回滚至此历史快照吗？\n\n回滚后配置将即时热更新生效。', {
        title: '恢复历史配置快照',
        confirmText: '确认恢复',
        icon: '↺',
        isWarning: true
      });
      if (!ok) return;
      try {
        const res = await authFetch('/api/config/rollback', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ id })
        });
        const data = await res.json();
        if (data.ok) {
          showToast('✅ 配置已成功回滚至所选快照并即时生效！');
          loadConfig();
          loadConfigBackups();
          loadStatus();
        } else {
          showToast('❌ 回滚失败: ' + (data.error || '未知错误'), true);
        }
      } catch (err) {
        showToast('❌ 回滚请求失败: ' + err.message, true);
      }
    }

    let currentBackendAccountMap = {};
    let availableCfAccounts = [];

    function renderBackendAccountMappingForm() {
      const container = document.getElementById('backend-accounts-mapping-container');
      if (!container) return;

      const wDomains = (document.getElementById('cfg-workerBackends')?.value || '')
        .split(',').map(s=>s.trim()).filter(Boolean);
      const pDomains = (document.getElementById('cfg-pagesBackends')?.value || '')
        .split(',').map(s=>s.trim()).filter(Boolean);

      if (wDomains.length === 0 && pDomains.length === 0) {
        container.innerHTML = '<div style="color: var(--text-muted); font-size: 0.85rem; padding: 0.5rem 0;">请先在上方输入 Worker 或 Pages 后端域名列表</div>';
        return;
      }

      let html = '';

      if (wDomains.length > 0) {
        html += '<div style="font-size: 0.82rem; font-weight: 700; color: #f59e0b; margin-top: 0.25rem;">🔶 Worker 后端域名关联:</div>';
        html += wDomains.map(d => {
          const curVal = currentBackendAccountMap[d] || '';
          return '<div style="display: flex; justify-content: space-between; align-items: center; gap: 0.75rem; background: rgba(255,255,255,0.02); padding: 0.45rem 0.75rem; border-radius: 8px; border: 1px solid var(--card-border); flex-wrap: wrap;">' +
            '<span style="font-family: monospace; font-size: 0.85rem; color: #fff;">🔶 ' + escapeHtml(d) + '</span>' +
            '<select class="form-control backend-account-select" data-domain="' + escapeHtml(d) + '" style="max-width: 280px; padding: 0.25rem 0.5rem; font-size: 0.8rem;">' +
              '<option value="">-- 未关联 CF 账户 --</option>' +
              (availableCfAccounts || []).map(a => {
                const idVal = a.id || a.accountId;
                const isSel = (idVal === curVal || a.accountId === curVal || a.name === curVal) ? 'selected' : '';
                const shortId = a.accountId ? ' (' + a.accountId.slice(0, 6) + '...)' : '';
                return '<option value="' + escapeHtml(idVal) + '" ' + isSel + '>' + escapeHtml((a.name || idVal) + shortId) + '</option>';
              }).join('') +
            '</select>' +
          '</div>';
        }).join('');
      }

      if (pDomains.length > 0) {
        html += '<div style="font-size: 0.82rem; font-weight: 700; color: #eab308; margin-top: 0.5rem;">⚡️ Pages 后端域名关联:</div>';
        html += pDomains.map(d => {
          const curVal = currentBackendAccountMap[d] || '';
          return '<div style="display: flex; justify-content: space-between; align-items: center; gap: 0.75rem; background: rgba(255,255,255,0.02); padding: 0.45rem 0.75rem; border-radius: 8px; border: 1px solid var(--card-border); flex-wrap: wrap;">' +
            '<span style="font-family: monospace; font-size: 0.85rem; color: #fff;">⚡️ ' + escapeHtml(d) + '</span>' +
            '<select class="form-control backend-account-select" data-domain="' + escapeHtml(d) + '" style="max-width: 280px; padding: 0.25rem 0.5rem; font-size: 0.8rem;">' +
              '<option value="">-- 未关联 CF 账户 --</option>' +
              (availableCfAccounts || []).map(a => {
                const idVal = a.id || a.accountId;
                const isSel = (idVal === curVal || a.accountId === curVal || a.name === curVal) ? 'selected' : '';
                const shortId = a.accountId ? ' (' + a.accountId.slice(0, 6) + '...)' : '';
                return '<option value="' + escapeHtml(idVal) + '" ' + isSel + '>' + escapeHtml((a.name || idVal) + shortId) + '</option>';
              }).join('') +
            '</select>' +
          '</div>';
        }).join('');
      }

      container.innerHTML = html;

      // 绑定 select 变更即时记忆
      container.querySelectorAll('.backend-account-select').forEach(sel => {
        sel.addEventListener('change', () => {
          currentBackendAccountMap[sel.dataset.domain] = sel.value;
        });
      });
    }

    function autoMatchBackendAccounts() {
      if (!availableCfAccounts || availableCfAccounts.length === 0) {
        showToast('⚠️ 尚未配置任何 Cloudflare 子账户', true);
        return;
      }
      let matchedCount = 0;
      document.querySelectorAll('.backend-account-select').forEach(sel => {
        const domain = (sel.dataset.domain || '').toLowerCase();
        for (const acc of availableCfAccounts) {
          const accName = (acc.name || '').toLowerCase();
          const cleanAccName = accName.replace(/@.*$/, '');
          if (cleanAccName && cleanAccName.length >= 3 && domain.includes(cleanAccName)) {
            sel.value = acc.id || acc.accountId;
            currentBackendAccountMap[sel.dataset.domain] = sel.value;
            matchedCount++;
            break;
          }
        }
      });
      showToast('⚡ 智能匹配完成，已自动关联 ' + matchedCount + ' 个后端！请点击下方保存生效');
    }

    async function loadConfig() {
      try {
        const res = await authFetch('/api/config');
        const data = await res.json();
        const defPref = document.getElementById('cfg-defaultPrefer');
        const wBack = document.getElementById('cfg-workerBackends');
        const pBack = document.getElementById('cfg-pagesBackends');
        const tgTok = document.getElementById('cfg-tgToken');
        const tgChat = document.getElementById('cfg-tgChatId');
        const tgProx = document.getElementById('cfg-tgProxy');
        const tgHost = document.getElementById('cfg-tgApiHost');

        if (defPref) defPref.value = data.defaultPrefer || '';
        if (wBack) wBack.value = (data.workerBackends || []).join(', ');
        if (pBack) pBack.value = (data.pagesBackends || []).join(', ');
        if (tgTok) tgTok.value = data.telegram?.botToken || '';
        if (tgChat) tgChat.value = data.telegram?.chatId || '';
        if (tgProx) tgProx.value = data.telegram?.proxy || '';
        if (tgHost) tgHost.value = data.telegram?.apiHost || 'api.telegram.org';

        if (data.quotaDisplayMode === 'total') {
          const rTotal = document.getElementById('quota-mode-total');
          if (rTotal) rTotal.checked = true;
        } else {
          const rProtocol = document.getElementById('quota-mode-protocol');
          if (rProtocol) rProtocol.checked = true;
        }

        if (data.loadBalanceMode === 'sticky') {
          const rSticky = document.getElementById('lb-mode-sticky');
          if (rSticky) rSticky.checked = true;
        } else {
          const rRr = document.getElementById('lb-mode-rr');
          if (rRr) rRr.checked = true;
        }

        const tfSample = document.getElementById('cfg-trafficSampleMinutes');
        if (tfSample) tfSample.value = data.trafficSampleMinutes || 5;

        const avoidThreshold = data.cfQuotaAvoidThreshold !== undefined ? data.cfQuotaAvoidThreshold : 95;
        const avoidEl = document.getElementById('cfg-cfQuotaAvoidThreshold');
        const avoidBadge = document.getElementById('cfg-avoid-threshold-badge');
        if (avoidEl) avoidEl.value = avoidThreshold;
        if (avoidBadge) avoidBadge.innerText = avoidThreshold + '%';

        const mg = data.memoryGuard || {};
        const elMgEnabled = document.getElementById('cfg-memory-enabled');
        const elMgThresh = document.getElementById('cfg-memory-threshold');
        const elMgInterval = document.getElementById('cfg-memory-interval');
        const elMgGcBadge = document.getElementById('cfg-memory-gc-badge');

        if (elMgEnabled) elMgEnabled.checked = mg.enabled !== false;
        if (elMgThresh) elMgThresh.value = mg.thresholdMB || 150;
        if (elMgInterval) elMgInterval.value = mg.intervalMinutes || 5;

        authFetch('/api/memory/status').then(r => r.json()).then(mRes => {
          if (mRes.ok && elMgGcBadge) {
            const isGc = mRes.data?.isGcExposed;
            elMgGcBadge.innerText = isGc ? '✅ V8 原生 GC 已就绪' : 'ℹ️ 标准回收模式 (启动参数可加 --expose-gc)';
            elMgGcBadge.style.color = isGc ? '#34d399' : '#fbbf24';
            elMgGcBadge.style.background = isGc ? 'rgba(16, 185, 129, 0.15)' : 'rgba(251, 191, 36, 0.15)';
          }
        }).catch(() => {});

        currentBackendAccountMap = data.backendAccountMap || {};
        if (data.cfDirect?.accounts && data.cfDirect.accounts.length > 0) {
          availableCfAccounts = data.cfDirect.accounts;
        } else if (!availableCfAccounts || availableCfAccounts.length === 0) {
          if (cfAccountsData && cfAccountsData.length > 0) {
            availableCfAccounts = cfAccountsData;
          }
        }
        renderBackendAccountMappingForm();

        // 兼容 UsagePanel 模式：若当前尚未缓存账户列表，主动拉取一次并即时填充映射下拉框
        if (!availableCfAccounts || availableCfAccounts.length === 0) {
          authFetch('/api/cf-panel/accounts').then(r => r.json()).then(res => {
            if (res.ok && Array.isArray(res.accounts) && res.accounts.length > 0) {
              availableCfAccounts = res.accounts;
              cfAccountsData = res.accounts;
              renderBackendAccountMappingForm();
            }
          }).catch(() => {});
        }

        // 监听文本框改变实时更新绑定表
        wBack?.removeEventListener('input', renderBackendAccountMappingForm);
        wBack?.addEventListener('input', renderBackendAccountMappingForm);
        pBack?.removeEventListener('input', renderBackendAccountMappingForm);
        pBack?.addEventListener('input', renderBackendAccountMappingForm);

        renderSubStoreScript();
        loadCfPanelTarget();
      } catch (e) {}
    }

    function renderSubStoreScript() {
      const host = window.location.hostname || '10.10.10.35';
      const el = document.getElementById('code-substore-script');
      if (!el) return;
      el.value = 
`/**
 * Sub-Store 节点转换操作脚本：自动对接局域网 Macvlan 双引擎分流网关
 * 逻辑：
 * - WS 节点 -> 指向 ${host}:8787 (原生 HTTP/1.1 WebSocket)
 * - XHTTP 节点 -> 指向 ${host}:8788 (原生 HTTP/2 / H1.1 自适应)
 * - 自动把优选 IP 与端口打包进 prefer 参数 (并清除末尾冗余斜杠)
 * - 原原本本保留节点原有的 Host、UUID 等所有自定义头部与信息
 */
function operator(proxies, targetPlatform) {
    const LOCAL_GATEWAY_IP = '${host}';
    const PORT_WS = 8787;
    const PORT_XHTTP = 8788;

    return proxies.map(proxy => {
        if (proxy.type && proxy.type.toLowerCase() === 'vless') {
            const originalServer = String(proxy.server || '').trim().replace(/\\/+$/, '');
            const originalPort = proxy.port || 443;
            const prefer = originalPort === 443 ? originalServer : \`\${originalServer}:\${originalPort}\`;

            // 判断是否为 XHTTP 协议节点
            const isXhttp = (proxy.network && (proxy.network.toLowerCase() === 'xhttp' || proxy.network.toLowerCase() === 'splithttp')) ||
                            proxy['xhttp-opts'] || proxy['splithttp-opts'];

            // 1. 设置目标 IP 与对应的专用引擎端口
            proxy.server = LOCAL_GATEWAY_IP;
            proxy.port = isXhttp ? PORT_XHTTP : PORT_WS;

            // 2. 局域网关闭 TLS
            proxy.tls = false;
            delete proxy.sni;
            delete proxy.ech;
            delete proxy.fingerprint;
            delete proxy.alpn;

            // 3. 将原本的优选服务器及端口注入到 Path 中，并清理冗余末尾斜杠
            const appendPrefer = (p) => {
                if (!p) return \`/?prefer=\${encodeURIComponent(prefer)}\`;
                const cleanP = p.replace(/\\/+$/, '');
                return cleanP.includes('?') 
                    ? \`\${cleanP}&prefer=\${encodeURIComponent(prefer)}\` 
                    : \`\${cleanP}?prefer=\${encodeURIComponent(prefer)}\`;
            };

            // 原原本本保留节点原有的 Host 与其它配置，不再删除任何 Host
            if (proxy['ws-opts']) {
                proxy['ws-opts'].path = appendPrefer(proxy['ws-opts'].path);
            }

            if (proxy['xhttp-opts']) {
                proxy['xhttp-opts'].path = appendPrefer(proxy['xhttp-opts'].path);
            }

            if (proxy['splithttp-opts']) {
                proxy['splithttp-opts'].path = appendPrefer(proxy['splithttp-opts'].path);
            }
        }
        return proxy;
    });
}`;
    }

    function copySubStoreScript() {
      const el = document.getElementById('code-substore-script');
      if (!el) return;
      copyToClipboard(el.value).then(() => {
        showToast('📋 Sub-Store JS 脚本已成功复制到剪贴板！');
      }).catch(() => {
        el.focus();
        el.select();
        prompt('请手动复制:', el.value);
      });
    }

    async function saveConfig() {
      const mapping = {};
      let hasValidSelect = false;
      document.querySelectorAll('.backend-account-select').forEach(sel => {
        const domain = sel.dataset.domain;
        const accId = sel.value;
        if (domain && accId) {
          mapping[domain] = accId;
          hasValidSelect = true;
        }
      });
      const finalMapping = hasValidSelect ? mapping : (currentBackendAccountMap || {});
      currentBackendAccountMap = finalMapping;

      const payload = {
        defaultPrefer: document.getElementById('cfg-defaultPrefer').value.trim(),
        workerBackends: document.getElementById('cfg-workerBackends').value.split(',').map(s=>s.trim()).filter(Boolean),
        pagesBackends: document.getElementById('cfg-pagesBackends').value.split(',').map(s=>s.trim()).filter(Boolean),
        trafficSampleMinutes: parseInt(document.getElementById('cfg-trafficSampleMinutes')?.value || '5', 10),
        cfQuotaAvoidThreshold: parseInt(document.getElementById('cfg-cfQuotaAvoidThreshold')?.value || '95', 10),
        quotaDisplayMode: document.querySelector('input[name="cfg-quotaDisplayMode"]:checked')?.value || 'protocol',
        loadBalanceMode: document.querySelector('input[name="cfg-loadBalanceMode"]:checked')?.value || 'round-robin',
        backendAccountMap: finalMapping,
        memoryGuard: {
          enabled: document.getElementById('cfg-memory-enabled') ? document.getElementById('cfg-memory-enabled').checked : true,
          thresholdMB: parseInt(document.getElementById('cfg-memory-threshold')?.value || '150', 10),
          intervalMinutes: parseInt(document.getElementById('cfg-memory-interval')?.value || '5', 10)
        },
        telegram: {
          botToken: document.getElementById('cfg-tgToken').value.trim(),
          chatId: document.getElementById('cfg-tgChatId').value.trim(),
          proxy: document.getElementById('cfg-tgProxy').value.trim(),
          apiHost: document.getElementById('cfg-tgApiHost').value.trim()
        }
      };
      const res = await authFetch('/api/config', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload)
      });
      const ret = await res.json();
      if (ret.ok) {
        showToast('✅ 配置保存成功，已即时热应用！');
        loadStatus();
      } else {
        showToast('❌ 保存失败: ' + ret.error, true);
      }
    }

    async function testTelegram() {
      const resBox = document.getElementById('tg-test-result');
      resBox.style.display = 'block';
      resBox.style.color = '#9ca3af';
      resBox.innerText = '正在通过通道发送测试消息...';
      try {
        const res = await authFetch('/api/test-tg', { method: 'POST' });
        const data = await res.json();
        if (data.ok) {
          resBox.style.color = '#34d399';
          resBox.innerText = '✅ 测试消息发送成功！请检查您的 Telegram 消息。';
        } else {
          resBox.style.color = '#f87171';
          resBox.innerText = '❌ 发送失败: ' + (data.error || '未知错误');
        }
      } catch (e) {
        resBox.style.color = '#f87171';
        resBox.innerText = '❌ 请求出错: ' + e.message;
      }
    }

    async function triggerResetCircuit() {
      const ok = await showConfirm('确认要一键解除所有后端的熔断隔离并重新入池吗？\n\n所有处于隔离状态的后端将被立即激活并重新参与轮询。', {
        title: '重置熔断状态',
        confirmText: '解除隔离',
        icon: '🔄'
      });
      if (!ok) return;
      await authFetch('/api/reset-circuit', { method: 'POST' });
      showToast('✅ 熔断状态已重置！');
      loadStatus();
    }

    async function triggerRestart() {
      const ok = await showConfirm('确定要重启网关服务吗？\n\nDocker 容器将在 1 秒内安全退出并由守护进程自动重新拉起。', {
        title: '重启网关服务',
        confirmText: '确认重启',
        icon: '⚡',
        isDanger: true
      });
      if (!ok) return;
      try {
        await authFetch('/api/restart', { method: 'POST' });
      } catch {}
      showToast('网关正在重启中，3 秒后自动刷新页面...');
      setTimeout(() => location.reload(), 3000);
    }

    async function triggerManualGcAction() {
      const btn = document.getElementById('btn-manual-gc');
      const originalText = btn ? btn.innerText : '⚡ 立即回收';
      if (btn) {
        btn.disabled = true;
        btn.innerText = '⏳ 回收中...';
      }
      try {
        const res = await authFetch('/api/memory/gc', { method: 'POST' });
        const json = await res.json();
        if (json.ok) {
          const freed = json.freedRssMB > 0 ? `${json.freedRssMB} MB` : (json.freedHeapMB > 0 ? `${json.freedHeapMB} MB (堆释放)` : '0 MB (已处极佳低位)');
          showToast(`✅ 深度内存回收完成！已释放 ${freed}`);
          loadStatus();
        } else {
          showToast('⚠️ 内存回收提示: ' + (json.error || '未能释放更多内存'), true);
        }
      } catch (err) {
        showToast('❌ 执行内存回收失败: ' + err.message, true);
      } finally {
        if (btn) {
          btn.disabled = false;
          btn.innerText = originalText;
        }
      }
    }
    window.triggerManualGcAction = triggerManualGcAction;

    

    
    // ================= 功能 2: 24 小时流量走势波形图 (Sparkline) =================
    function renderSparkline(svgId, dataPoints, strokeColor, gradId) {
      const svg = document.getElementById(svgId);
      if (!svg) return;
      if (!dataPoints || dataPoints.length < 2) return;

      const width = 280;
      const height = 36;
      const padding = 3;

      const maxVal = Math.max(...dataPoints, 5);
      const minVal = 0;
      const range = maxVal - minVal || 1;

      const points = dataPoints.map((val, idx) => {
        const x = padding + (idx / (dataPoints.length - 1)) * (width - 2 * padding);
        const y = height - padding - ((val - minVal) / range) * (height - 2 * padding);
        return { x: Math.round(x * 10) / 10, y: Math.round(y * 10) / 10 };
      });

      const linePath = points.map((p, i) => (i === 0 ? `M ${p.x} ${p.y}` : `L ${p.x} ${p.y}`)).join(' ');
      const firstP = points[0];
      const lastP = points[points.length - 1];
      const areaPath = `${linePath} L ${lastP.x} ${height} L ${firstP.x} ${height} Z`;

      svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
      svg.setAttribute('preserveAspectRatio', 'none');
      svg.innerHTML = `
        <defs>
          <linearGradient id="${gradId}" x1="0" y1="0" x2="0" y2="1">
            <stop offset="0%" stop-color="${strokeColor}" stop-opacity="0.38"/>
            <stop offset="100%" stop-color="${strokeColor}" stop-opacity="0.0"/>
          </linearGradient>
        </defs>
        <path d="${areaPath}" fill="url(#${gradId})" />
        <path d="${linePath}" fill="none" stroke="${strokeColor}" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" />
        <circle cx="${lastP.x}" cy="${lastP.y}" r="3" fill="${strokeColor}" />
      `;
    }

    function updateTrafficSparklines(trafficHistory, curXhttp, curWs) {
      curXhttp = Number(curXhttp) || 0;
      curWs = Number(curWs) || 0;

      let xhttpPoints = [];
      let wsPoints = [];

      if (Array.isArray(trafficHistory) && trafficHistory.length >= 2) {
        xhttpPoints = trafficHistory.map(h => (h.xhttp !== undefined ? h.xhttp : (h.deltaXhttp || 0)));
        wsPoints = trafficHistory.map(h => (h.ws !== undefined ? h.ws : (h.deltaWs || 0)));
        xhttpPoints.push(curXhttp);
        wsPoints.push(curWs);
      } else {
        // 当后端历史采样未积累时，根据实时当前值生成 14 个自然阶梯波动基线，确保首屏立即可见！
        const count = 14;
        for (let i = 0; i < count; i++) {
          const ratio = (i + 1) / count;
          const jitterX = (i % 3 === 0 ? 0.8 : (i % 2 === 0 ? 1.1 : 0.95));
          const jitterW = (i % 3 === 1 ? 0.85 : (i % 2 === 1 ? 1.15 : 0.9));
          xhttpPoints.push(Math.max(1, Math.round(curXhttp * ratio * jitterX)));
          wsPoints.push(Math.max(1, Math.round(curWs * ratio * jitterW)));
        }
        xhttpPoints.push(curXhttp);
        wsPoints.push(curWs);
      }

      renderSparkline('sparkline-xhttp', xhttpPoints, '#06b6d4', 'gradXhttp');
      renderSparkline('sparkline-ws', wsPoints, '#8b5cf6', 'gradWs');
    }

    // ================= 主题切换 (Dark / Light) =================
    function initTheme() {
      const savedTheme = localStorage.getItem('edgetunnel_theme') || 'dark';
      applyTheme(savedTheme);
    }

    function toggleTheme() {
      const isLight = document.body.classList.contains('light-theme');
      const targetTheme = isLight ? 'dark' : 'light';
      applyTheme(targetTheme);
      localStorage.setItem('edgetunnel_theme', targetTheme);
      showToast(targetTheme === 'light' ? '☀️ 已切换至明亮浅色主题' : '🌙 已切换至暗黑科技主题');
    }

    function applyTheme(theme) {
      const btn = document.getElementById('theme-toggle-btn');
      const sunIcon = document.getElementById('theme-sun-icon');
      const moonIcon = document.getElementById('theme-moon-icon');
      const btnText = document.getElementById('theme-btn-text');

      if (theme === 'light') {
        // 当前为浅色模式 -> 按钮展示“深色模式”与月亮图标，提示点击切回深色
        document.body.classList.add('light-theme');
        if (sunIcon) sunIcon.style.display = 'none';
        if (moonIcon) moonIcon.style.display = 'block';
        if (btnText) btnText.innerText = '深色模式';
        if (btn) btn.title = '切换至暗黑深色模式';
      } else {
        // 当前为深色模式 -> 按钮展示“浅色模式”与太阳图标，提示点击切回浅色
        document.body.classList.remove('light-theme');
        if (sunIcon) sunIcon.style.display = 'block';
        if (moonIcon) moonIcon.style.display = 'none';
        if (btnText) btnText.innerText = '浅色模式';
        if (btn) btn.title = '切换至明亮浅色模式';
      }
    }

    async function manualRefreshDashboard() {
      const btn = document.getElementById('dash-refresh-btn');
      if (btn) {
        btn.disabled = true;
        btn.innerHTML = '⏳ 刷新中...';
      }
      try {
        await loadStatus();
        const badge = document.getElementById('dashboard-last-update');
        if (badge) badge.innerText = '刚刚更新 (' + new Date().toLocaleTimeString([], { hour:'2-digit', minute:'2-digit', second:'2-digit' }) + ')';
        showToast('已刷新监控中心最新指标数据');
      } catch (e) {
        showToast('刷新失败: ' + e.message, true);
      } finally {
        if (btn) {
          btn.disabled = false;
          btn.innerHTML = '🔄 刷新数据';
        }
      }
    }

    // ================= Cloudflare 多账户独立额度管理 =================
    let cfAccountsData = null;
    let isCfIframeVisible = false;

    // ================= Cloudflare 额度双轨配置 (模式 A / 模式 B) =================
    let currentCfQuotaMode = 'usagepanel';

    function applyCfQuotaMode(mode) {
      currentCfQuotaMode = mode || 'usagepanel';
      const modeABtns = document.getElementById('cf-mode-a-btns');
      const toggleBtn = document.getElementById('cf-toggle-iframe-btn');
      const iframeSec = document.getElementById('cf-iframe-section');

      if (currentCfQuotaMode === 'direct') {
        // 模式 B: 官方直连，完全隐藏原版后台与新窗口打开相关入口
        if (modeABtns) modeABtns.style.display = 'none';
        if (toggleBtn) toggleBtn.style.display = 'none';
        if (iframeSec) {
          iframeSec.style.display = 'none';
          isCfIframeVisible = false;
        }
      } else {
        // 模式 A: UsagePanel 镜像模式，显示原版后台切换与新窗口打开组合操作
        if (modeABtns) modeABtns.style.display = 'inline-flex';
        if (toggleBtn) {
          toggleBtn.style.display = 'inline-flex';
          toggleBtn.innerText = isCfIframeVisible ? '🖥️ 隐藏原版后台' : '🖥️ 切换原版后台';
        }
      }
    }

    function switchCfConfigMode(mode, syncMain = true) {
      currentCfQuotaMode = mode;
      const btnA = document.getElementById('cf-tab-mode-usagepanel');
      const btnB = document.getElementById('cf-tab-mode-direct');
      const paneA = document.getElementById('cf-pane-usagepanel');
      const paneB = document.getElementById('cf-pane-direct');

      if (btnA && btnB) {
        if (mode === 'usagepanel') {
          btnA.className = 'btn btn-primary btn-sm';
          btnB.className = 'btn btn-outline btn-sm';
          if (paneA) paneA.style.display = 'block';
          if (paneB) paneB.style.display = 'none';
        } else {
          btnA.className = 'btn btn-outline btn-sm';
          btnB.className = 'btn btn-primary btn-sm';
          if (paneA) paneA.style.display = 'none';
          if (paneB) paneB.style.display = 'block';
        }
      }

      if (syncMain) {
        applyCfQuotaMode(mode);
      }
    }

    async function removeDirectAccountRow(rowId) {
      const row = document.getElementById(rowId);
      if (!row) return;
      const nameInp = row.querySelector('.cf-dir-name')?.value.trim() || '';
      const accIdInp = row.querySelector('.cf-dir-accid')?.value.trim() || '';
      const desc = nameInp || (accIdInp ? ('Account ID: ' + accIdInp.slice(0, 8) + '...') : '该账户');
      const ok = await showConfirm('确定要删除【' + desc + '】的直连账户配置吗？', {
        title: '删除账户配置',
        confirmText: '确认删除',
        icon: '🗑️',
        isDanger: true
      });
      if (ok) {
        row.remove();
      }
    }

    function addDirectAccountRow(data = null) {
      const container = document.getElementById('cf-direct-accounts-container');
      if (!container) return;

      const rowId = 'cf-direct-row-' + Date.now() + '-' + Math.floor(Math.random() * 1000);
      const row = document.createElement('div');
      row.className = 'cf-direct-account-row';
      row.id = rowId;
      row.style.cssText = 'display: grid; grid-template-columns: 180px 1fr 1fr 40px; gap: 0.5rem; align-items: center; background: rgba(255,255,255,0.02); padding: 0.5rem 0.75rem; border-radius: 8px; border: 1px solid var(--card-border);';

      const nameVal = data?.name ? escapeHtml(data.name) : '';
      const accIdVal = data?.accountId ? escapeHtml(data.accountId) : '';
      const tokenPlaceholder = data?.hasToken ? '已配置 (留空表示不修改)' : 'API Token (Bearer)';

      row.innerHTML = 
        '<input type="text" class="form-control cf-dir-name" style="font-size: 0.82rem;" placeholder="账户备注 (如 yourname@example.com)" value="' + nameVal + '">' +
        '<input type="text" class="form-control cf-dir-accid" style="font-size: 0.82rem; font-family: monospace;" placeholder="Account ID (32位)" value="' + accIdVal + '">' +
        '<input type="password" class="form-control cf-dir-token" style="font-size: 0.82rem;" placeholder="' + tokenPlaceholder + '">' +
        '<button type="button" class="btn btn-outline btn-sm" onclick="removeDirectAccountRow(\'' + rowId + '\')" style="padding: 0.35rem 0.45rem; color: #fca5a5; border-color: rgba(239,68,68,0.3);" title="删除此账户">🗑️</button>';

      container.appendChild(row);
    }

    function toggleCfAuthDrawer() {
      const d = document.getElementById('cf-auth-drawer');
      if (!d) return;
      const isHidden = d.style.display === 'none';
      d.style.display = isHidden ? 'block' : 'none';
      if (isHidden) {
        loadCfCredentialsConfig();
      }
    }

    async function loadCfCredentialsConfig() {
      try {
        const res = await authFetch('/api/config');
        const data = await res.json();
        const mode = data.cfQuotaMode || 'usagepanel';
        applyCfQuotaMode(mode);
        switchCfConfigMode(mode, false);
        const cf = data.cfPanel || {};
        const urlInp = document.getElementById('cf-cfg-url');
        const userInp = document.getElementById('cf-cfg-user');
        const passInp = document.getElementById('cf-cfg-pass');
        if (urlInp) urlInp.value = cf.url || localStorage.getItem('cf_real_panel_url') || 'https://usagepanel.pages.dev';
        if (userInp) userInp.value = cf.username || '';
        if (passInp && cf.hasPassword) passInp.placeholder = '•••••••• (已保存，留空则不修改)';

        // 加载模式 B 官方直连账户列表
        const dContainer = document.getElementById('cf-direct-accounts-container');
        if (dContainer) {
          dContainer.innerHTML = '';
          const dAccs = data.cfDirect?.accounts || [];
          if (dAccs.length > 0) {
            dAccs.forEach(acc => addDirectAccountRow(acc));
          } else {
            addDirectAccountRow();
          }
        }

        // 加载告警阈值
        const thVal = data.cfAlertThreshold || cf.alertThreshold || 80;
        const thInp = document.getElementById('cf-cfg-alert-threshold');
        const thBadge = document.getElementById('cf-cfg-threshold-badge');
        if (thInp) thInp.value = thVal;
        if (thBadge) thBadge.innerText = thVal + '%';
      } catch {}
    }

    async function saveCfCredentials() {
      const msgBox = document.getElementById('cf-auth-status-msg');
      const thInp = document.getElementById('cf-cfg-alert-threshold');
      const alertThreshold = thInp ? Math.max(1, Math.min(100, parseInt(thInp.value, 10) || 80)) : 80;

      if (msgBox) {
        msgBox.style.display = 'block';
        msgBox.style.background = 'rgba(59, 130, 246, 0.15)';
        msgBox.style.color = '#93c5fd';
        msgBox.style.border = '1px solid rgba(59, 130, 246, 0.3)';
        msgBox.innerText = '正在验证凭据并同步额度数据，请稍候...';
      }

      try {
        if (currentCfQuotaMode === 'direct') {
          // 模式 B: 官方 API 直连保存
          const rows = document.querySelectorAll('.cf-direct-account-row');
          const accounts = [];
          rows.forEach((row, i) => {
            const name = row.querySelector('.cf-dir-name')?.value.trim() || '';
            const accountId = row.querySelector('.cf-dir-accid')?.value.trim() || '';
            const apiToken = row.querySelector('.cf-dir-token')?.value.trim() || '';
            if (accountId || name) {
              accounts.push({ id: 'acc_' + i, name, accountId, apiToken });
            }
          });

          if (accounts.length === 0) {
            showToast('请至少添加一个包含 Account ID 的子账户', true);
            if (msgBox) msgBox.style.display = 'none';
            return;
          }

          const res = await authFetch('/api/cf-config', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({
              mode: 'direct',
              alertThreshold,
              direct: { accounts }
            })
          });
          if (!res.ok) {
            const raw = await res.text();
            let msg = raw;
            try { const j = JSON.parse(raw); msg = j.error || j.message || raw; } catch {}
            if (res.status === 404) {
              throw new Error('新接口尚未加载 (HTTP 404)。请先点击页面右上角【🔄 重启网关】！');
            }
            throw new Error(msg);
          }
          const data = await res.json();
          if (data.ok) {
            if (msgBox) {
              msgBox.style.background = 'rgba(16, 185, 129, 0.15)';
              msgBox.style.color = '#34d399';
              msgBox.style.border = '1px solid rgba(16, 185, 129, 0.3)';
              msgBox.innerText = '✅ 官方直连凭据与告警阈值 (' + alertThreshold + '%) 保存成功！';
            }
            showToast('🎉 Cloudflare 官方 API 凭据已成功保存！');
            setTimeout(() => { toggleCfAuthDrawer(); loadCfAccounts(true); }, 1000);
          } else {
            if (msgBox) {
              msgBox.style.background = 'rgba(239, 68, 68, 0.15)';
              msgBox.style.color = '#fca5a5';
              msgBox.style.border = '1px solid rgba(239, 68, 68, 0.3)';
              msgBox.innerText = '❌ 保存失败: ' + (data.error || '未知错误');
            }
          }
        } else {
          // 模式 A: cmliu UsagePanel 镜像拉取保存
          const urlInp = document.getElementById('cf-cfg-url');
          const userInp = document.getElementById('cf-cfg-user');
          const passInp = document.getElementById('cf-cfg-pass');

          let url = urlInp ? urlInp.value.trim() : '';
          if (!url) url = 'https://usagepanel.pages.dev';
          if (!url.startsWith('http://') && !url.startsWith('https://')) url = 'https://' + url;

          const username = userInp ? userInp.value.trim() : '';
          const password = passInp ? passInp.value : '';

          if (!username) {
            showToast('请输入管理员账号', true);
            if (msgBox) msgBox.style.display = 'none';
            return;
          }

          const res = await authFetch('/api/cf-panel/credentials', {
            method: 'POST',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ url, username, password, alertThreshold })
          });
          let data;
          try {
            data = await res.json();
          } catch (e) {
            throw new Error('网关响应异常 (HTTP ' + res.status + ')。请先在右上角点击【重启网关】让新接口生效！');
          }

          if (data.ok) {
            localStorage.setItem('cf_real_panel_url', url);
            if (msgBox) {
              msgBox.style.background = 'rgba(16, 185, 129, 0.15)';
              msgBox.style.color = '#34d399';
              msgBox.style.border = '1px solid rgba(16, 185, 129, 0.3)';
              msgBox.innerText = '✅ 凭据验证成功！已同步 ' + (data.data?.accounts?.length || 0) + ' 个子账户数据 (告警阈值: ' + alertThreshold + '%)。';
            }
            showToast('🎉 CF 面板凭据与告警阈值已成功保存！');
            setTimeout(() => { toggleCfAuthDrawer(); loadCfAccounts(true); }, 1000);
          } else {
            if (msgBox) {
              msgBox.style.background = 'rgba(239, 68, 68, 0.15)';
              msgBox.style.color = '#fca5a5';
              msgBox.style.border = '1px solid rgba(239, 68, 68, 0.3)';
              msgBox.innerText = '❌ 验证失败: ' + (data.error || '账号或密码错误');
            }
          }
        }
      } catch (err) {
        if (msgBox) {
          msgBox.style.background = 'rgba(239, 68, 68, 0.15)';
          msgBox.style.color = '#fca5a5';
          msgBox.style.border = '1px solid rgba(239, 68, 68, 0.3)';
          msgBox.innerText = '❌ 请求异常: ' + err.message;
        }
      }
    }

    async function loadCfAccounts(force = false) {
      const grid = document.getElementById('cf-accounts-grid');
      if (force && grid) {
        grid.innerHTML = '<div class="card" style="grid-column: 1 / -1; text-align: center; padding: 2.5rem 1rem;">' +
          '<div style="font-size: 2rem; margin-bottom: 0.5rem; animation: pulse 1.5s infinite;">☁️</div>' +
          '<div style="color: var(--text-muted); font-size: 0.9rem;">正在拉取 Cloudflare 各账户最新额度...</div></div>';
      }

      try {
        const res = await authFetch('/api/cf-panel/accounts' + (force ? '?force=1' : ''));
        const data = await res.json();
        if (data.needAuth) {
          renderCfNeedAuthView();
          return;
        }
        if (data.ok && Array.isArray(data.accounts)) {
          cfAccountsData = data.accounts;
          if (!availableCfAccounts || availableCfAccounts.length === 0) {
            availableCfAccounts = data.accounts;
            renderBackendAccountMappingForm();
          }
          if (data.mode) applyCfQuotaMode(data.mode);
          renderSummaryCard(data.accounts);
          renderSubAccountsCards(data.accounts);
          const badge = document.getElementById('cf-account-count-badge');
          if (badge) badge.innerText = data.accounts.length + ' 个账户在线';
          if (force) showToast('已成功刷新全部 ' + data.accounts.length + ' 个子账户额度');
        } else {
          if (grid) {
            grid.innerHTML = '<div class="card" style="grid-column: 1 / -1; text-align: center; padding: 2rem;">' +
              '<div style="color: #f87171; font-size: 1rem; margin-bottom: 0.5rem;">⚠️ 获取子账户数据失败</div>' +
              '<div style="color: var(--text-muted); font-size: 0.85rem;">' + (data.error || '请检查面板凭据是否正确') + '</div>' +
              '<button class="btn btn-primary btn-sm" style="margin-top:1rem;" onclick="toggleCfAuthDrawer()">重新配置凭据</button></div>';
          }
        }
      } catch (err) {
        if (grid) {
          grid.innerHTML = '<div class="card" style="grid-column: 1 / -1; text-align: center; padding: 2rem;">' +
            '<div style="color: #f87171; font-size: 1rem;">网络请求异常: ' + err.message + '</div></div>';
        }
      }
    }

    function renderCfNeedAuthView() {
      const grid = document.getElementById('cf-accounts-grid');
      if (!grid) return;
      grid.innerHTML = '<div class="card" style="grid-column: 1 / -1; text-align: center; padding: 3rem 1.5rem; border: 1px dashed rgba(255,255,255,0.2);">' +
        '<div style="font-size: 2.75rem; margin-bottom: 0.75rem;">🔐</div>' +
        '<h3 style="font-size: 1.25rem; font-weight: 700; color: #fff; margin-bottom: 0.5rem;">尚未配置 CF 面板管理员凭据</h3>' +
        '<p style="color: var(--text-muted); font-size: 0.85rem; max-width: 500px; margin: 0 auto 1.5rem auto;">' +
        '将您的 UsagePanel 账号密码配置到 EdgeTunnel 后，系统将自动在后台免密同步全部 6 个子账户各自独立的请求用量与资源消耗，并支持超额预警。' +
        '</p>' +
        '<button class="btn btn-primary" onclick="toggleCfAuthDrawer()">⚙️ 立即配置面板账号密码</button>' +
        '</div>';
    }

            // ================= 资源额度细节 3 卡片渲染器 (完全还原官方结构，防挤压两行自适应) =================
    function formatBytes(bytes) {
      if (!bytes || bytes <= 0) return '0 B';
      const k = 1024;
      const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
      const i = Math.floor(Math.log(bytes) / Math.log(k));
      if (i === 0) return bytes + ' B';
      return (bytes / Math.pow(k, i)).toFixed(2) + ' ' + sizes[i];
    }

    function renderResourceItem(label, val, limit, isBytes = false) {
      val = Number(val) || 0;
      limit = Number(limit) || 0;
      const pct = limit > 0 ? ((val / limit) * 100).toFixed(1) : '0.0';
      const valStr = isBytes ? formatBytes(val) : val.toLocaleString();
      const limitStr = isBytes ? formatBytes(limit) : limit.toLocaleString();

      let barColor = '#10b981';
      const numPct = parseFloat(pct);
      if (numPct >= 80) barColor = '#ef4444';
      else if (numPct >= 50) barColor = '#f59e0b';

      return '<div style="margin-bottom: 0.6rem;">' +
        '<div style="display: flex; justify-content: space-between; align-items: baseline; font-size: 0.75rem; gap: 0.25rem;">' +
          '<span style="color: var(--text-muted); white-space: nowrap; flex-shrink: 0;">' + label + '</span>' +
          '<span style="color: ' + barColor + '; font-weight: 700; font-size: 0.75rem; white-space: nowrap;">' + pct + '%</span>' +
        '</div>' +
        '<div style="font-family: monospace; font-size: 0.73rem; color: #fff; text-align: right; margin-top: 1px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis;" class="acc-val" title="' + valStr + ' / ' + limitStr + '">' +
          valStr + ' / ' + limitStr +
        '</div>' +
        '<div style="height: 3px; background: rgba(255,255,255,0.06); border-radius: 2px; overflow: hidden; margin-top: 3px;">' +
          '<div style="height: 100%; width: ' + Math.min(100, Math.max(0, numPct)) + '%; background: ' + barColor + '; border-radius: 2px;"></div>' +
        '</div>' +
      '</div>';
    }

    function renderResourceDetailsHtml(r) {
      const kv = r?.kv || {};
      const d1 = r?.d1 || {};
      const r2 = r?.r2 || {};

      const kvDeletesLists = (Number(kv.deletes) || 0) + (Number(kv.lists) || 0);
      const kvDeletesListsLimit = (Number(kv.deletesLimit) || 1000) + (Number(kv.listsLimit) || 1000);

      return '<div style="display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 0.65rem; margin-top: 0.85rem;" class="res-detail-grid">' +
        '<!-- KV 卡片 -->' +
        '<div class="acc-box" style="background: rgba(15, 23, 42, 0.6); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.75rem 0.65rem; overflow: hidden;">' +
          '<div style="display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 0.65rem; border-bottom: 1px solid rgba(255,255,255,0.06); padding-bottom: 0.4rem; gap: 0.25rem;">' +
            '<span style="font-weight: 700; font-size: 0.95rem; color: #fff;" class="acc-res-title">KV</span>' +
            '<span style="font-size: 0.72rem; color: var(--text-muted); white-space: nowrap;">' + (kv.namespaces || 0) + ' 个命名空间</span>' +
          '</div>' +
          renderResourceItem('读取（今日）', kv.reads || 0, kv.readsLimit || 100000) +
          renderResourceItem('写入（今日）', kv.writes || 0, kv.writesLimit || 1000) +
          renderResourceItem('删除/列表（今日）', kvDeletesLists, kvDeletesListsLimit) +
          renderResourceItem('存储', kv.storageBytes || 0, kv.storageLimitBytes || 1073741824, true) +
        '</div>' +

        '<!-- D1 卡片 -->' +
        '<div class="acc-box" style="background: rgba(15, 23, 42, 0.6); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.75rem 0.65rem; overflow: hidden;">' +
          '<div style="display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 0.65rem; border-bottom: 1px solid rgba(255,255,255,0.06); padding-bottom: 0.4rem; gap: 0.25rem;">' +
            '<span style="font-weight: 700; font-size: 0.95rem; color: #fff;" class="acc-res-title">D1</span>' +
            '<span style="font-size: 0.72rem; color: var(--text-muted); white-space: nowrap;">' + (d1.databases || 0) + ' 个数据库</span>' +
          '</div>' +
          renderResourceItem('读取（今日）', d1.rowsRead || 0, d1.rowsReadLimit || 5000000) +
          renderResourceItem('写入（今日）', d1.rowsWritten || 0, d1.rowsWrittenLimit || 100000) +
          renderResourceItem('存储', d1.storageBytes || 0, d1.storageLimitBytes || 5368709120, true) +
        '</div>' +

        '<!-- R2 卡片 -->' +
        '<div class="acc-box" style="background: rgba(15, 23, 42, 0.6); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.75rem 0.65rem; overflow: hidden;">' +
          '<div style="display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 0.65rem; border-bottom: 1px solid rgba(255,255,255,0.06); padding-bottom: 0.4rem; gap: 0.25rem;">' +
            '<span style="font-weight: 700; font-size: 0.95rem; color: #fff;" class="acc-res-title">R2</span>' +
            '<span style="font-size: 0.72rem; color: var(--text-muted); white-space: nowrap;">' + (r2.buckets || 0) + ' 个存储桶</span>' +
          '</div>' +
          renderResourceItem('Class A（本月）', r2.classA || 0, r2.classALimit || 1000000) +
          renderResourceItem('Class B（本月）', r2.classB || 0, r2.classBLimit || 10000000) +
          renderResourceItem('存储', r2.storageBytes || 0, r2.storageLimitBytes || 10737418240, true) +
        '</div>' +
      '</div>' +
      '<div style="font-size: 0.72rem; color: var(--text-muted); margin-top: 0.65rem; text-align: right;" class="res-footer-note">' +
        'D1/KV 按 UTC 自然日统计，R2 操作按本月统计；存储为最近一次指标快照。' +
      '</div>';
    }

    function renderSummaryCard(accounts) {
      const container = document.getElementById('cf-summary-container');
      if (!container) return;
      if (!accounts || accounts.length === 0) {
        container.innerHTML = '';
        return;
      }

      const totalWorkers = accounts.reduce((sum, a) => sum + (a.workers || 0), 0);
      const totalPages = accounts.reduce((sum, a) => sum + (a.pages || 0), 0);
      const totalRequests = totalWorkers + totalPages;
      const maxRequests = accounts.reduce((sum, a) => sum + (a.max || 100000), 0);
      const totalPercent = maxRequests > 0 ? (totalRequests / maxRequests * 100) : 0;

      // 聚合所有账户的资源数据
      const summaryResources = {
        kv: {
          namespaces: accounts.reduce((sum, a) => sum + (a.resources?.kv?.namespaces || 0), 0),
          reads: accounts.reduce((sum, a) => sum + (a.resources?.kv?.reads || 0), 0),
          readsLimit: accounts.reduce((sum, a) => sum + (a.resources?.kv?.readsLimit || 100000), 0),
          writes: accounts.reduce((sum, a) => sum + (a.resources?.kv?.writes || 0), 0),
          writesLimit: accounts.reduce((sum, a) => sum + (a.resources?.kv?.writesLimit || 1000), 0),
          deletes: accounts.reduce((sum, a) => sum + (a.resources?.kv?.deletes || 0), 0),
          lists: accounts.reduce((sum, a) => sum + (a.resources?.kv?.lists || 0), 0),
          deletesLimit: accounts.reduce((sum, a) => sum + (a.resources?.kv?.deletesLimit || 1000), 0),
          listsLimit: accounts.reduce((sum, a) => sum + (a.resources?.kv?.listsLimit || 1000), 0),
          storageBytes: accounts.reduce((sum, a) => sum + (a.resources?.kv?.storageBytes || 0), 0),
          storageLimitBytes: accounts.reduce((sum, a) => sum + (a.resources?.kv?.storageLimitBytes || 1073741824), 0)
        },
        d1: {
          databases: accounts.reduce((sum, a) => sum + (a.resources?.d1?.databases || 0), 0),
          rowsRead: accounts.reduce((sum, a) => sum + (a.resources?.d1?.rowsRead || 0), 0),
          rowsReadLimit: accounts.reduce((sum, a) => sum + (a.resources?.d1?.rowsReadLimit || 5000000), 0),
          rowsWritten: accounts.reduce((sum, a) => sum + (a.resources?.d1?.rowsWritten || 0), 0),
          rowsWrittenLimit: accounts.reduce((sum, a) => sum + (a.resources?.d1?.rowsWrittenLimit || 100000), 0),
          storageBytes: accounts.reduce((sum, a) => sum + (a.resources?.d1?.storageBytes || 0), 0),
          storageLimitBytes: accounts.reduce((sum, a) => sum + (a.resources?.d1?.storageLimitBytes || 5368709120), 0)
        },
        r2: {
          buckets: accounts.reduce((sum, a) => sum + (a.resources?.r2?.buckets || 0), 0),
          classA: accounts.reduce((sum, a) => sum + (a.resources?.r2?.classA || 0), 0),
          classALimit: accounts.reduce((sum, a) => sum + (a.resources?.r2?.classALimit || 1000000), 0),
          classB: accounts.reduce((sum, a) => sum + (a.resources?.r2?.classB || 0), 0),
          classBLimit: accounts.reduce((sum, a) => sum + (a.resources?.r2?.classBLimit || 10000000), 0),
          storageBytes: accounts.reduce((sum, a) => sum + (a.resources?.r2?.storageBytes || 0), 0),
          storageLimitBytes: accounts.reduce((sum, a) => sum + (a.resources?.r2?.storageLimitBytes || 10737418240), 0)
        }
      };

      let summaryPctColor = '#10b981';
      let summaryBarGrad = '#10b981';
      if (totalPercent >= 85) {
        summaryPctColor = '#ef4444';
        summaryBarGrad = '#ef4444';
      } else if (totalPercent >= 60) {
        summaryPctColor = '#f59e0b';
        summaryBarGrad = '#f59e0b';
      }

      container.innerHTML = '<div class="card" style="padding: 1.5rem 1.6rem; border-radius: 16px; background: var(--card-bg); border: 1px solid var(--card-border);">' +
        '<div style="font-size: 1.3rem; font-weight: 700; color: #fff; margin-bottom: 1.25rem;" class="acc-title">' +
          'Cloudflare 额度汇总' +
        '</div>' +

        '<!-- 总请求占比 -->' +
        '<div style="margin-bottom: 1.35rem;">' +
          '<div style="display: flex; justify-content: space-between; align-items: baseline; margin-bottom: 0.5rem;">' +
            '<span style="font-size: 0.95rem; color: var(--text-muted); font-weight: 600;">总请求占比</span>' +
            '<span style="font-size: 1.55rem; font-weight: 800; color: ' + summaryPctColor + ';">' + totalPercent.toFixed(1) + '%</span>' +
          '</div>' +
          '<div style="height: 10px; background: rgba(255,255,255,0.08); border-radius: 999px; overflow: hidden; margin-bottom: 0.45rem;">' +
            '<div style="height: 100%; width: ' + Math.min(100, Math.max(2, totalPercent)) + '%; background: ' + summaryBarGrad + '; border-radius: 999px; transition: width 0.4s ease;"></div>' +
          '</div>' +
          '<div style="text-align: right; font-size: 0.86rem; color: var(--text-muted); font-family: monospace;">' +
            '<span style="font-weight: 700; color: #fff;" class="acc-val">' + totalRequests.toLocaleString() + '</span> / ' + maxRequests.toLocaleString() + ' 总计请求' +
          '</div>' +
        '</div>' +

        '<!-- 核心云服务指标概览 (Workers, Pages, KV, D1, R2) -->' +
        '<div style="display: grid; grid-template-columns: repeat(auto-fit, minmax(180px, 1fr)); gap: 0.85rem; margin-bottom: 1.25rem;">' +
          '<!-- Workers -->' +
          '<div class="acc-box" style="background: rgba(30, 41, 59, 0.45); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.9rem 1.1rem; display: flex; align-items: center; gap: 0.9rem;">' +
            '<div style="width: 38px; height: 38px; background: rgba(245, 158, 11, 0.15); border: 1px solid rgba(245, 158, 11, 0.3); border-radius: 10px; display: flex; align-items: center; justify-content: center; flex-shrink: 0;">' +
              '<span style="color: #f59e0b; font-size: 1.2rem; line-height: 1;">🔶</span>' +
            '</div>' +
            '<div>' +
              '<div style="font-size: 0.72rem; color: var(--text-muted); font-weight: 700; letter-spacing: 0.05em;">WORKERS</div>' +
              '<div style="font-size: 1.35rem; font-weight: 800; color: #fff;" class="acc-val">' + totalWorkers.toLocaleString() + '</div>' +
            '</div>' +
          '</div>' +
          '<!-- Pages -->' +
          '<div class="acc-box" style="background: rgba(30, 41, 59, 0.45); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.9rem 1.1rem; display: flex; align-items: center; gap: 0.9rem;">' +
            '<div style="width: 38px; height: 38px; background: rgba(234, 179, 8, 0.15); border: 1px solid rgba(234, 179, 8, 0.3); border-radius: 10px; display: flex; align-items: center; justify-content: center; flex-shrink: 0;">' +
              '<span style="color: #eab308; font-size: 1.2rem; line-height: 1;">⚡️</span>' +
            '</div>' +
            '<div>' +
              '<div style="font-size: 0.72rem; color: var(--text-muted); font-weight: 700; letter-spacing: 0.05em;">PAGES</div>' +
              '<div style="font-size: 1.35rem; font-weight: 800; color: #fff;" class="acc-val">' + totalPages.toLocaleString() + '</div>' +
            '</div>' +
          '</div>' +
          '<!-- D1 -->' +
          '<div class="acc-box" style="background: rgba(30, 41, 59, 0.45); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.9rem 1.1rem; display: flex; align-items: center; gap: 0.9rem;">' +
            '<div style="width: 38px; height: 38px; background: rgba(59, 130, 246, 0.15); border: 1px solid rgba(59, 130, 246, 0.3); border-radius: 10px; display: flex; align-items: center; justify-content: center; flex-shrink: 0;">' +
              '<span style="color: #60a5fa; font-size: 1.2rem; line-height: 1;">🗄️</span>' +
            '</div>' +
            '<div>' +
              '<div style="font-size: 0.72rem; color: var(--text-muted); font-weight: 700; letter-spacing: 0.05em;">D1 SQL</div>' +
              '<div style="font-size: 1.2rem; font-weight: 800; color: #fff;" class="acc-val">' + summaryResources.d1.databases + ' <span style="font-size: 0.8rem; font-weight: 500; color: var(--text-muted);">库 / ' + formatBytes(summaryResources.d1.storageBytes) + '</span></div>' +
            '</div>' +
          '</div>' +
          '<!-- R2 -->' +
          '<div class="acc-box" style="background: rgba(30, 41, 59, 0.45); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.9rem 1.1rem; display: flex; align-items: center; gap: 0.9rem;">' +
            '<div style="width: 38px; height: 38px; background: rgba(168, 85, 247, 0.15); border: 1px solid rgba(168, 85, 247, 0.3); border-radius: 10px; display: flex; align-items: center; justify-content: center; flex-shrink: 0;">' +
              '<span style="color: #c084fc; font-size: 1.2rem; line-height: 1;">🪣</span>' +
            '</div>' +
            '<div>' +
              '<div style="font-size: 0.72rem; color: var(--text-muted); font-weight: 700; letter-spacing: 0.05em;">R2 存储</div>' +
              '<div style="font-size: 1.2rem; font-weight: 800; color: #fff;" class="acc-val">' + summaryResources.r2.buckets + ' <span style="font-size: 0.8rem; font-weight: 500; color: var(--text-muted);">桶 / ' + formatBytes(summaryResources.r2.storageBytes) + '</span></div>' +
            '</div>' +
          '</div>' +
          '<!-- KV -->' +
          '<div class="acc-box" style="background: rgba(30, 41, 59, 0.45); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 12px; padding: 0.9rem 1.1rem; display: flex; align-items: center; gap: 0.9rem;">' +
            '<div style="width: 38px; height: 38px; background: rgba(16, 185, 129, 0.15); border: 1px solid rgba(16, 185, 129, 0.3); border-radius: 10px; display: flex; align-items: center; justify-content: center; flex-shrink: 0;">' +
              '<span style="color: #10b981; font-size: 1.2rem; line-height: 1;">🔑</span>' +
            '</div>' +
            '<div>' +
              '<div style="font-size: 0.72rem; color: var(--text-muted); font-weight: 700; letter-spacing: 0.05em;">KV 存储</div>' +
              '<div style="font-size: 1.2rem; font-weight: 800; color: #fff;" class="acc-val">' + summaryResources.kv.namespaces + ' <span style="font-size: 0.8rem; font-weight: 500; color: var(--text-muted);">空间 / ' + formatBytes(summaryResources.kv.storageBytes) + '</span></div>' +
            '</div>' +
          '</div>' +
        '</div>' +

        '<!-- 汇总资源额度细节抽屉 (KV / D1 / R2 三列横向展开) -->' +
        '<div class="acc-box" style="background: rgba(30, 41, 59, 0.45); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 10px; padding: 0.85rem 1.15rem;">' +
          '<div style="display: flex; justify-content: space-between; align-items: center;">' +
            '<div style="font-weight: 700; font-size: 0.92rem; color: #fff;" class="acc-res-title">资源详细配额（日/月使用量 & 存储）</div>' +
            '<div style="display: flex; align-items: center; gap: 0.75rem;">' +
              '<span style="font-size: 0.82rem; color: var(--text-muted); font-family: monospace;">KV / D1 / R2</span>' +
              '<button class="btn btn-primary btn-sm" onclick="toggleAccDetail(\'summary\')" style="background: #6366f1; border-color: #6366f1; font-size: 0.78rem; padding: 0.3rem 0.85rem; border-radius: 8px;" id="btn-detail-summary">展开细节</button>' +
            '</div>' +
          '</div>' +
          '<div id="detail-summary" style="display: none;">' +
            renderResourceDetailsHtml(summaryResources) +
          '</div>' +
        '</div>' +
      '</div>';
    }

function renderSubAccountsCards(accounts) {
      const grid = document.getElementById('cf-accounts-grid');
      if (!grid) return;
      if (!accounts || accounts.length === 0) {
        grid.innerHTML = '<div class="card" style="grid-column: 1 / -1; text-align: center; padding: 2.5rem 1rem; color: var(--text-muted);">' +
          '面板中尚未添加任何 Cloudflare 账号' +
          '</div>';
        return;
      }

      grid.innerHTML = accounts.map((acc, index) => {
        const pct = acc.percent || 0;
        let pctColor = '#10b981';
        let barGrad = '#10b981';
        let badgeClass = 'badge-success';
        let badgeText = '🟢 充足';

        if (pct >= 85) {
          pctColor = '#ef4444';
          barGrad = '#ef4444';
          badgeClass = 'badge-danger';
          badgeText = '🔴 告警';
        } else if (pct >= 60) {
          pctColor = '#f59e0b';
          barGrad = '#f59e0b';
          badgeClass = 'badge-warning';
          badgeText = '🟡 正常';
        }

        let timeStr = '--';
        if (acc.updateTime) {
          const d = new Date(acc.updateTime);
          const pad = n => String(n).padStart(2, '0');
          timeStr = d.getFullYear() + '/' + pad(d.getMonth() + 1) + '/' + pad(d.getDate()) + ' ' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
        }

        const safeName = escapeHtml(acc.name || '未命名账户');
        const safeId = escapeHtml(acc.accountId || '未分配');
        const cardDomId = 'acc-card-' + (acc.id || index);

        const rD1 = acc.resources?.d1 || {};
        const rR2 = acc.resources?.r2 || {};
        const rKv = acc.resources?.kv || {};

        return '<div class="card" style="padding: 1.25rem 1.4rem; border-radius: 14px; position: relative; display: flex; flex-direction: column; justify-content: space-between; gap: 1rem; background: var(--card-bg); border: 1px solid var(--card-border);">' +
          '<div>' +
            '<div style="display: flex; justify-content: space-between; align-items: flex-start; gap: 0.75rem; margin-bottom: 0.85rem;">' +
              '<div style="flex: 1; min-width: 0;">' +
                '<div class="acc-title" style="font-weight: 700; font-size: 1.12rem; color: #fff; display: flex; align-items: center; gap: 0.45rem; word-break: break-all;">' +
                  '<span>🔑</span> <span>' + safeName + '</span>' +
                '</div>' +
                '<div style="font-size: 0.84rem; color: var(--text-muted); margin-top: 0.35rem; display: flex; align-items: flex-start; gap: 0.4rem; font-family: monospace; word-break: break-all; overflow-wrap: anywhere;">' +
                  '<span style="flex-shrink: 0; margin-top: 1px;">🔒</span> <span style="word-break: break-all; overflow-wrap: anywhere;">AccountId: ' + safeId + '</span>' +
                '</div>' +
                '<div style="font-size: 0.8rem; color: var(--text-muted); margin-top: 0.3rem; display: flex; align-items: center; gap: 0.4rem;">' +
                  '<span>🕒</span> <span>更新时间: ' + timeStr + '</span>' +
                '</div>' +
              '</div>' +
              '<span class="badge ' + badgeClass + '" style="font-size: 0.75rem; padding: 0.2rem 0.55rem; white-space: nowrap;">' + badgeText + '</span>' +
            '</div>' +

            '<div style="margin: 1rem 0 0.5rem 0;">' +
              '<div style="display: flex; justify-content: space-between; align-items: baseline; font-size: 0.88rem; margin-bottom: 0.5rem; flex-wrap: wrap; gap: 0.5rem;">' +
                '<div class="acc-total-text" style="font-size: 0.88rem;">' +
                  '<span style="color: var(--text-muted);">请求使用情况: </span>' +
                  '<span style="font-weight: 700; color: #fff;" class="acc-val">' + (acc.total || 0).toLocaleString() + ' / ' + (acc.max || 100000).toLocaleString() + '</span> ' +
                  '<span style="font-weight: 700; color: ' + pctColor + ';">' + pct.toFixed(1) + '%</span>' +
                '</div>' +
              '</div>' +
              '<div style="height: 8px; background: rgba(255,255,255,0.08); border-radius: 999px; overflow: hidden; margin-bottom: 0.85rem;">' +
                '<div style="height: 100%; width: ' + Math.min(100, Math.max(2, pct)) + '%; background: ' + barGrad + '; border-radius: 999px; transition: width 0.4s ease;"></div>' +
              '</div>' +
              '<!-- 直观云服务指标徽章 -->' +
              '<div style="display: flex; flex-wrap: wrap; gap: 0.4rem; font-size: 0.78rem;">' +
                '<span class="badge" style="background: rgba(245, 158, 11, 0.12); color: #f59e0b; border: 1px solid rgba(245, 158, 11, 0.25); padding: 0.22rem 0.55rem;">🔶 Workers: ' + (acc.workers || 0).toLocaleString() + '</span>' +
                '<span class="badge" style="background: rgba(234, 179, 8, 0.12); color: #eab308; border: 1px solid rgba(234, 179, 8, 0.25); padding: 0.22rem 0.55rem;">⚡️ Pages: ' + (acc.pages || 0).toLocaleString() + '</span>' +
                '<span class="badge" style="background: rgba(59, 130, 246, 0.12); color: #60a5fa; border: 1px solid rgba(59, 130, 246, 0.25); padding: 0.22rem 0.55rem;">🗄️ D1: ' + (rD1.databases || 0) + '库 (' + formatBytes(rD1.storageBytes || 0) + ')</span>' +
                '<span class="badge" style="background: rgba(168, 85, 247, 0.12); color: #c084fc; border: 1px solid rgba(168, 85, 247, 0.25); padding: 0.22rem 0.55rem;">🪣 R2: ' + (rR2.buckets || 0) + '桶 (' + formatBytes(rR2.storageBytes || 0) + ')</span>' +
                '<span class="badge" style="background: rgba(16, 185, 129, 0.12); color: #10b981; border: 1px solid rgba(16, 185, 129, 0.25); padding: 0.22rem 0.55rem;">🔑 KV: ' + (rKv.namespaces || 0) + '空间 (' + formatBytes(rKv.storageBytes || 0) + ')</span>' +
              '</div>' +
            '</div>' +
          '</div>' +

          '<!-- 资源额度细节抽屉 (KV / D1 / R2 三列横向展开) -->' +
          '<div class="acc-box" style="background: rgba(30, 41, 59, 0.45); border: 1px solid rgba(255, 255, 255, 0.08); border-radius: 10px; padding: 0.75rem 1rem;">' +
            '<div style="display: flex; justify-content: space-between; align-items: center;">' +
              '<div style="font-weight: 700; font-size: 0.9rem; color: #fff;" class="acc-res-title">详细配额与操作量</div>' +
              '<div style="display: flex; align-items: center; gap: 0.75rem;">' +
                '<span style="font-size: 0.8rem; color: var(--text-muted); font-family: monospace;">KV / D1 / R2</span>' +
                '<button class="btn btn-primary btn-sm" onclick="toggleAccDetail(\'' + cardDomId + '\')" style="background: #6366f1; border-color: #6366f1; font-size: 0.75rem; padding: 0.25rem 0.75rem; border-radius: 8px;" id="btn-' + cardDomId + '">展开细节</button>' +
              '</div>' +
            '</div>' +
            '<div id="detail-' + cardDomId + '" style="display: none;">' +
              renderResourceDetailsHtml(acc.resources) +
            '</div>' +
          '</div>' +
        '</div>';
      }).join('');
    }

    function toggleAccDetail(cardDomId) {
      const detailDiv = document.getElementById('detail-' + cardDomId);
      const btn = document.getElementById('btn-' + cardDomId) || document.getElementById('btn-detail-' + cardDomId);
      if (!detailDiv) return;
      const isHidden = detailDiv.style.display === 'none';
      detailDiv.style.display = isHidden ? 'block' : 'none';
      if (btn) btn.innerText = isHidden ? '收起细节' : '展开细节';
    }

    function toggleCfIframeView() {
      if (currentCfQuotaMode === 'direct') {
        showToast('当前处于模式 B (官方 API 直连)，无需使用原版后台', true);
        return;
      }
      const sec = document.getElementById('cf-iframe-section');
      const btn = document.getElementById('cf-toggle-iframe-btn');
      if (!sec) return;
      isCfIframeVisible = !isCfIframeVisible;
      sec.style.display = isCfIframeVisible ? 'block' : 'none';
      if (btn) btn.innerText = isCfIframeVisible ? '🖥️ 隐藏原版后台' : '🖥️ 切换原版后台';
      if (isCfIframeVisible) {
        ensureCfUsageLoaded();
        setTimeout(() => {
          sec.scrollIntoView({ behavior: 'smooth', block: 'start' });
        }, 80);
      }
    }

    function ensureCfUsageLoaded() {
      const iframe = document.getElementById('cf-usage-iframe');
      if (!iframe) return;
      const urlTag = document.getElementById('cf-iframe-url-tag');
      const urlInp = document.getElementById('cf-cfg-url');
      const urlVal = (urlInp && urlInp.value ? urlInp.value.trim() : '') || localStorage.getItem('cf_real_panel_url') || 'https://usagepanel.pages.dev';
      if (urlTag) urlTag.innerText = urlVal;

      if (!iframe.src || iframe.src === 'about:blank' || iframe.src === window.location.href) {
        const loader = document.getElementById('cf-iframe-loader');
        if (loader) loader.style.display = 'flex';
        iframe.src = '/api/cf-panel/';
      }
    }

    function reloadCfIframe() {
      const iframe = document.getElementById('cf-usage-iframe');
      const loader = document.getElementById('cf-iframe-loader');
      if (iframe) {
        if (loader) loader.style.display = 'flex';
        iframe.src = '/api/cf-panel/?t=' + Date.now();
      }
    }

    function onCfIframeLoaded() {
      const loader = document.getElementById('cf-iframe-loader');
      const iframe = document.getElementById('cf-usage-iframe');
      if (iframe && iframe.src && iframe.src !== 'about:blank') {
        if (loader) loader.style.display = 'none';
      }
    }

    // 内联 onload 早于本脚本执行（about:blank 即时 load），必须在此处绑定
    const cfUsageIframeEl = document.getElementById('cf-usage-iframe');
    if (cfUsageIframeEl) cfUsageIframeEl.addEventListener('load', onCfIframeLoaded);

    function openCfUsageExternal() {
      const url = localStorage.getItem('cf_real_panel_url') || 'https://usagepanel.pages.dev';
      window.open(url, '_blank');
    }

    function escapeHtml(str) {
      if (!str) return '';
      return String(str).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }


        // ================= 功能 3: 实时日志关键词过滤与错误级别筛选 =================
    let currentLogLevelFilter = 'all';

    function setLogLevelFilter(level) {
      currentLogLevelFilter = level;
      ['all', 'error', 'warn'].forEach(l => {
        const btn = document.getElementById('btn-filter-' + l);
        if (btn) {
          if (l === level) {
            btn.style.borderColor = 'var(--primary)';
            btn.style.color = 'var(--primary)';
            btn.style.fontWeight = '700';
          } else {
            btn.style.borderColor = '';
            btn.style.color = '';
            btn.style.fontWeight = '';
          }
        }
      });
      applyLogFilter();
    }

    function applyLogFilter() {
      const query = (document.getElementById('log-search-filter')?.value || '').toLowerCase().trim();
      const term = document.getElementById('terminal');
      if (!term) return;

      const lines = term.getElementsByClassName('log-line');
      let visibleCount = 0;
      for (const line of lines) {
        const text = line.innerText.toLowerCase();
        const level = line.getAttribute('data-level') || 'info';

        let matchesLevel = true;
        if (currentLogLevelFilter === 'error') {
          matchesLevel = (level === 'error' || text.includes('429') || text.includes('1101') || text.includes('502') || text.includes('fail'));
        } else if (currentLogLevelFilter === 'warn') {
          matchesLevel = (level === 'warn' || level === 'error');
        }

        const matchesQuery = !query || text.includes(query);

        if (matchesLevel && matchesQuery) {
          line.style.display = '';
          visibleCount++;
        } else {
          line.style.display = 'none';
        }
      }

      const countBadge = document.getElementById('log-count-badge');
      if (countBadge) {
        countBadge.innerText = (query || currentLogLevelFilter !== 'all') ? (visibleCount + ' / ' + lines.length + ' 条') : (lines.length + ' 条日志');
      }
    }

    function appendLogItem(log, term) {
      if (!log || typeof log.message !== 'string') return;
      const line = document.createElement('div');
      line.className = 'log-line';
      line.setAttribute('data-level', log.level || 'info');
      let colorClass = 'log-info';
      if (log.level === 'error') colorClass = 'log-error';
      if (log.level === 'warn') colorClass = 'log-warn';

      const cleanMsg = log.message.replace(/\x1b\[[0-9;]*m/g, '');
      const timeStr = log.time || new Date().toLocaleTimeString('zh-CN', { timeZone: 'Asia/Shanghai', hour12: false });
      line.innerHTML = '<span class="log-time">[' + escapeHtml(timeStr) + ']</span><span class="' + colorClass + '">' + escapeHtml(cleanMsg) + '</span>';

      // 实时匹配当前关键词与级别过滤
      const query = (document.getElementById('log-search-filter')?.value || '').toLowerCase().trim();
      const lineText = (timeStr + ' ' + cleanMsg).toLowerCase();
      let matchesLevel = true;
      if (currentLogLevelFilter === 'error') {
        matchesLevel = (log.level === 'error' || lineText.includes('429') || lineText.includes('1101') || lineText.includes('502') || lineText.includes('fail'));
      } else if (currentLogLevelFilter === 'warn') {
        matchesLevel = (log.level === 'warn' || log.level === 'error');
      }
      if (!(matchesLevel && (!query || lineText.includes(query)))) {
        line.style.display = 'none';
      }

      term.appendChild(line);

      // 保持最多 600 条以保护 DOM 性能
      if (term.childNodes.length > 600) {
        term.removeChild(term.firstChild);
      }

      const countBadge = document.getElementById('log-count-badge');
      if (countBadge && (!query && currentLogLevelFilter === 'all')) {
        countBadge.innerText = term.childNodes.length + ' 条日志';
      }

      if (!isPausedLog && line.style.display !== 'none') {
        term.scrollTop = term.scrollHeight;
      }
    }

    function initLogStream() {
      if (eventSourceInstance) {
        try { eventSourceInstance.close(); } catch {}
      }
      const token = localStorage.getItem('edgetunnel_token');
      const sseUrl = '/api/logs/stream' + (token ? '?token=' + encodeURIComponent(token) : '');
      const term = document.getElementById('terminal');
      if (!term) return;

      const es = new EventSource(sseUrl);
      eventSourceInstance = es;

      es.onmessage = (e) => {
        try {
          if (!e.data) return;
          const data = JSON.parse(e.data);

          // 1. 初始化全量历史日志
          if (data.type === 'init' && Array.isArray(data.logs)) {
            term.innerHTML = '';
            for (const item of data.logs) {
              appendLogItem(item, term);
            }
            return;
          }

          // 2. 实时增量日志
          if (data.type === 'log' && data.log) {
            appendLogItem(data.log, term);
            return;
          }

          // 3. 兼容直接发送单条日志对象
          if (data.message !== undefined) {
            appendLogItem(data, term);
          }
        } catch (err) {
          console.error('[Web Console Log Parse Error]:', err);
        }
      };

      es.onerror = () => {
        const countBadge = document.getElementById('log-count-badge');
        if (countBadge && (!term.childNodes.length)) {
          countBadge.innerText = '连接中断，重连中...';
        }
      };
    }

    function clearLogs() {
      const term = document.getElementById('terminal');
      if (term) term.innerHTML = '';
      const countBadge = document.getElementById('log-count-badge');
      if (countBadge) countBadge.innerText = '0 条日志';
    }

    function togglePauseLog() {
      isPausedLog = !isPausedLog;
      const btn = document.getElementById('btn-pause-log');
      if (btn) btn.innerText = isPausedLog ? '继续滚屏' : '暂停滚屏';
    }

    function startApp() {
      updateAuthBtnState();
      loadStatus();
      const token = localStorage.getItem('edgetunnel_token');
      if (token) {
        loadConfig();
        renderSubStoreScript();
        initLogStream();
      }
      if (!statusInterval) {
        statusInterval = setInterval(loadStatus, 3000);
      }
    }

    initTheme();
    updateAuthBtnState();
    async function initApp() {
      try {
        const res = await fetch('/api/auth/status');
        const data = await res.json();
        if (data.needSetup) {
          showAuthModal(true);
          return;
        }

        const token = localStorage.getItem('edgetunnel_token');
        if (token) {
          const checkRes = await fetch('/api/auth/status', {
            headers: { 'Authorization': 'Bearer ' + token }
          });
          const checkData = await checkRes.json();
          if (checkData.authenticated) {
            applyAuthState(true);
            startApp();
            return;
          }
          localStorage.removeItem('edgetunnel_token');
        }

        // 若当前未处于登录态，但本地保存了凭据，全自动尝试静默登录！
        const savedCreds = getSavedCredentials();
        if (savedCreds && savedCreds.username && savedCreds.password) {
          try {
            const loginRes = await fetch('/api/auth/login', {
              method: 'POST',
              headers: { 'Content-Type': 'application/json' },
              body: JSON.stringify({ username: savedCreds.username, password: savedCreds.password })
            });
            const loginData = await loginRes.json();
            if (loginData.ok && loginData.token) {
              localStorage.setItem('edgetunnel_token', loginData.token);
              applyAuthState(true);
              hideAuthModal(loginData.username);
              startApp();
              return;
            }
          } catch (e) {
            console.warn('[Auto-Login] 静默登录异常:', e.message);
          }
        }

        // 未记住凭据或自动登录失败：平滑进入只读监控大盘
        applyAuthState(false);
        startApp();
      } catch (e) {
        applyAuthState(false);
        startApp();
      }
    }

    initApp();
