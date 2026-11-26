(function () {
  'use strict';

  function $(id) { return document.getElementById(id); }

  var msgTimer = null;
  function showMsg(text, isErr) {
    var el = $('msg');
    el.textContent = text;
    el.className = isErr ? 'err' : '';
    if (msgTimer) clearTimeout(msgTimer);
    msgTimer = setTimeout(function () { el.textContent = ''; el.className = ''; }, 8000);
  }

  // 主题切换：绿/白/暗/红四种自由换，存在 localStorage，刷新后还在。
  var THEMES = ['green', 'white', 'dark', 'red'];
  function applyTheme(name) {
    var t = THEMES.indexOf(name) >= 0 ? name : 'green';
    document.documentElement.setAttribute('data-theme', t);
    try { localStorage.setItem('djmihomo-theme', t); } catch (e) {}
    var dots = document.querySelectorAll('[data-theme-btn]');
    for (var i = 0; i < dots.length; i++) {
      dots[i].classList.toggle('active', dots[i].getAttribute('data-theme-btn') === t);
    }
  }
  (function initTheme() {
    var saved = null;
    try { saved = localStorage.getItem('djmihomo-theme'); } catch (e) {}
    applyTheme(saved || 'green');
    var dots = document.querySelectorAll('[data-theme-btn]');
    for (var i = 0; i < dots.length; i++) {
      dots[i].addEventListener('click', function () { applyTheme(this.getAttribute('data-theme-btn')); });
    }
  })();

  function api(path, method, body) {
    var opts = { method: method || 'GET', headers: {} };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body);
    }
    return fetch(path, opts).then(function (r) {
      return r.json().catch(function () { return { ok: false, error: 'HTTP ' + r.status }; });
    });
  }

  function esc(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;');
  }

  // ---- tabs ----
  var currentTab = 'app';
  var statusTimer = null;
  var logTimer = null;
  var connTimer = null;
  document.querySelectorAll('#tabs button').forEach(function (btn) {
    btn.addEventListener('click', function () { switchTab(btn.getAttribute('data-tab')); });
  });
  function switchTab(name) {
    currentTab = name;
    // 记住当前菜单：刷新后回到同一个菜单，不再跳回第一个
    try { localStorage.setItem('djmihomo-tab', name); } catch (e) {}
    document.querySelectorAll('#tabs button').forEach(function (b) {
      b.classList.toggle('active', b.getAttribute('data-tab') === name);
    });
    document.querySelectorAll('.tab').forEach(function (s) {
      s.classList.toggle('active', s.id === 'tab-' + name);
    });
    if (name === 'app') { loadStatus(); loadAppSettings(); loadConnectivity(); }
    if (name === 'profiles') { loadProfiles(); loadSubs(); }
    if (name === 'mixin') { loadMixin(); loadMixinFile(); }
    if (name === 'proxy') { loadMode(); loadProxyGroups(); }
    if (name === 'editor') { loadEditorProfiles(); loadEditor(); }
    if (name === 'live') ensureLiveFrame();
    if (name === 'logs') loadLogs();
  }
  statusTimer = setInterval(function () {
    if (currentTab === 'app' && document.visibilityState === 'visible') loadStatus();
  }, 5000);
  logTimer = setInterval(function () {
    if (currentTab === 'logs' && document.visibilityState === 'visible') loadLogs();
  }, 2000);
  // 连通性测试默认每 5 分钟自动跑一次（只在应用配置页打开且可见时），
  // 不通的站点会把处方写进运行日志。不想等就点【开始测试】手动测。
  connTimer = setInterval(function () {
    if (currentTab === 'app' && document.visibilityState === 'visible') loadConnectivity();
  }, 300000);

  // ---- app config ----
  var wasRunning = null; // 上一次看到的核心状态
  var ctlPort = 9090; // 控制端口（实时面板地址用）
  var appVersion = ''; // 实时面板 iframe 缓存戳：版本变了才换地址，防旧面板 JS 一直缓存
  function loadStatus() {
    api('/api/status').then(function (st) {
      $('st-appver').textContent = st.appVersion || '—';
      if (st.appVersion) appVersion = st.appVersion;
      $('st-corever').textContent = st.coreVersion || st.version || '—';
      var run = $('st-running');
      run.textContent = st.running ? '运行中' : '已停止';
      run.className = st.running ? 'ok' : 'bad';
      $('st-pid').textContent = st.pid || '—';
      $('st-mixed').textContent = st.mixedPort != null ? st.mixedPort : '—';
      $('st-ctl').textContent = st.controllerPort != null ? st.controllerPort : '—';
      if (st.controllerPort != null) ctlPort = st.controllerPort;
      $('st-mode').textContent = st.mode || '—';
      // 核心刚从停止变成运行中，且本页还没测过：自动跑一次连通性测试
      if (st.running && wasRunning === false && currentTab === 'app' && !connTested) {
        setTimeout(loadConnectivity, 1500);
      }
      wasRunning = !!st.running;
    }).catch(function () { showMsg('状态加载失败', true); });
    api('/api/proxies').then(function (d) {
      var keys = d && d.proxies ? Object.keys(d.proxies).length : 0;
      $('st-proxies').textContent = keys;
    }).catch(function () { $('st-proxies').textContent = '—'; });
  }
  // 连通性测试：国内直连、国外走代理，显示通不通和延迟
  var connIcons = { '百度': ['百', '#2932e1'], '阿里云': ['阿', '#ff6a00'], 'Google': ['G', '#4285f4'], 'YouTube': ['Y', '#ff0000'], 'GitHub': ['G', '#24292f'] };
  var connTested = false; // 本次打开页面后是否已测过
  var connNoteTimer = null; // 提示轮换定时器
  // 列表里的不通原因只显示短码，不把 Get "url": ... 的长错误全贴出来。
  function shortErr(e) {
    if (!e) return '';
    if (e.length <= 12) return e;
    var m = e.match(/":\s*(.+)$/);
    var r = m ? m[1] : e;
    if (/deadline exceeded/i.test(r)) return '超时';
    if (/EOF/.test(r)) return 'EOF';
    if (/connection refused/i.test(r)) return '连接被拒';
    if (/connection reset/i.test(r)) return '连接重置';
    if (/no such host/i.test(r)) return 'DNS失败';
    return r.length > 10 ? r.slice(0, 10) : r;
  }
  // 设置轮换提示文字：超长时把文字复制一份无缝横向滚动显示全文，不截断。
  function setConnNote(text) {
    var note = $('conn-note');
    var inner = $('conn-note-inner');
    if (!note || !inner) { return; }
    inner.style.animationDuration = '';
    note.classList.remove('scrolling');
    inner.textContent = text;
    void note.offsetWidth; // 让 class 移除生效后再量宽度
    if (inner.scrollWidth > note.clientWidth) {
      inner.textContent = text + '　　　' + text;
      var dur = Math.max(4, Math.min(12, (inner.scrollWidth / 2) / 60));
      inner.style.animationDuration = dur + 's';
      note.classList.add('scrolling');
    }
  }
  // 提示轮换：先显示“国内直连 · 国外走代理”，再逐个轮到每个站走哪条线，
  // 每 3 秒换一条，循环显示。
  function startConnNoteRotation(results) {
    if (connNoteTimer) { clearInterval(connNoteTimer); connNoteTimer = null; }
    var msgs = ['国内直连 · 国外走代理'];
    (results || []).forEach(function (r) {
      var s = r.name + '：' + (r.ok ? '通 · ' + r.ms + 'ms' : '不通' + (r.error ? ' · ' + r.error : ''));
      // 只显示最后一段线路（最终出口节点/DIRECT），不然整条链太长会换行。
      if (r.route) {
        var hops = r.route.split('→');
        var last = hops[hops.length - 1].trim();
        if (last) { s += ' · ' + last; }
      }
      msgs.push(s);
    });
    if (msgs.length <= 1) {
      setConnNote(msgs[0]);
      return;
    }
    var i = 0;
    setConnNote(msgs[0]);
    connNoteTimer = setInterval(function () {
      i = (i + 1) % msgs.length;
      setConnNote(msgs[i]);
    }, 3000);
  }
  function loadConnectivity() {
    var list = $('conn-list');
    if (connNoteTimer) { clearInterval(connNoteTimer); connNoteTimer = null; }
    setConnNote('测试中……');
    $('btn-conn-test').disabled = true;
    api('/api/connectivity').then(function (d) {
      $('btn-conn-test').disabled = false;
      if (!d.ok) {
        setConnNote(d.error || '测试失败');
        return;
      }
      connTested = true;
      startConnNoteRotation(d.results);
      list.innerHTML = (d.results || []).map(function (r) {
        var icon = connIcons[r.name] || ['?', '#6b7280'];
        var via = r.direct ? '直连' : '代理';
        var text, cls;
        if (r.ok) { text = '通 · ' + r.ms + ' ms'; cls = 'ok'; }
        else { text = '不通' + (r.error ? ' · ' + shortErr(r.error) : ''); cls = 'bad'; }
        var row = '<div class="conn-row"><span class="conn-icon" style="background:' + icon[1] + '">' + icon[0] + '</span>' +
          '<span class="conn-name">' + esc(r.name) + '</span>' +
          '<span class="conn-via">' + via + '</span>' +
          '<span class="conn-result ' + cls + '">' + esc(text) + '</span></div>';
        return row;
      }).join('');
    }).catch(function () {
      $('btn-conn-test').disabled = false;
      setConnNote('测试失败');
    });
  }
  $('btn-conn-test').addEventListener('click', loadConnectivity);
  function serviceAction(action, label) {
    showMsg('正在' + label + '……');
    api('/api/service', 'POST', { action: action }).then(function (r) {
      if (r.ok) {
        showMsg(label + '完成，当前状态：' + (r.running ? '运行中' : '已停止'));
        // 核心起来后自动跑一次连通性测试（等 2 秒让核心就绪）
        if (r.running && currentTab === 'app') setTimeout(loadConnectivity, 2000);
      } else {
        showMsg('失败：' + (r.error || '未知错误'), true);
      }
      loadStatus();
    }).catch(function () { showMsg('请求失败', true); });
  }
  $('btn-reload').addEventListener('click', function () { serviceAction('reload', '重载服务'); });
  $('btn-restart').addEventListener('click', function () { serviceAction('restart', '重启服务'); });
  $('btn-update-dash').addEventListener('click', function () {
    showMsg('正在更新面板……');
    api('/api/dashboard/update', 'POST', {}).then(function (r) {
      showMsg(r.ok ? (r.note || '面板已是最新') : '失败：' + (r.error || '未知错误'), !r.ok);
    }).catch(function () { showMsg('请求失败', true); });
  });
  $('btn-restore-ange').addEventListener('click', function () {
    showMsg('正在恢复 AnGe 面板……');
    api('/api/dashboard/restore-ange', 'POST', {}).then(function (r) {
      showMsg(r.ok ? '已恢复 AnGe 面板，刷新实时面板即可看到' : '失败：' + (r.error || '未知错误'), !r.ok);
      if (r.ok && currentTab === 'live') ensureLiveFrame();
    }).catch(function () { showMsg('请求失败', true); });
  });
  $('btn-open-dash').addEventListener('click', function () {
    api('/api/dashboard/url').then(function (d) {
      if (d && d.url) window.open(d.url, '_blank');
    }).catch(function () {
      window.open('http://' + window.location.hostname + ':' + ctlPort + '/ui/', '_blank');
    });
  });

  function fillProfileSelect(sel, profiles, active) {
    sel.innerHTML = profiles.map(function (p) {
      var ref = (p.type === 'subscription' ? 'subscription:' : 'file:') + p.name;
      var label = (p.type === 'subscription' ? '订阅：' : '文件：') + p.name;
      return '<option value="' + esc(ref) + '"' + (ref === active ? ' selected' : '') + '>' + esc(label) + '</option>';
    }).join('');
  }
  function loadAppSettings() {
    api('/api/app').then(function (a) {
      $('app-enabled').checked = !!a.enabled;
      $('app-start-delay').value = a.start_delay != null ? a.start_delay : 0;
      $('app-sched-restart').checked = !!a.scheduled_restart;
      $('app-sched-cron').value = a.scheduled_restart_cron || '0 3 * * *';
      $('app-core-only').checked = !!a.core_only;
      return api('/api/profiles').then(function (d) {
        fillProfileSelect($('app-profile'), d.profiles || [], a.profile || d.active);
      });
    }).catch(function () { showMsg('应用配置加载失败', true); });
  }
  $('app-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var body = {
      enabled: $('app-enabled').checked,
      profile: $('app-profile').value,
      start_delay: parseInt($('app-start-delay').value, 10) || 0,
      scheduled_restart: $('app-sched-restart').checked,
      scheduled_restart_cron: $('app-sched-cron').value.trim() || '0 3 * * *',
      core_only: $('app-core-only').checked
    };
    showMsg('正在保存应用配置……');
    api('/api/app', 'PUT', body).then(function (r) {
      showMsg(r.ok ? '已保存' : '失败：' + (r.error || '未知错误'), !r.ok);
      loadStatus();
    }).catch(function () { showMsg('请求失败', true); });
  });

  // ---- profiles & subscriptions ----
  function loadProfiles() {
    api('/api/profiles').then(function (d) {
      var tbody = document.querySelector('#profile-table tbody');
      var list = (d && d.profiles) || [];
      var active = d.active || '';
      if (!list.length) {
        tbody.innerHTML = '<tr><td colspan="5">暂无配置文件</td></tr>';
        return;
      }
      tbody.innerHTML = list.map(function (p) {
        var ref = (p.type === 'subscription' ? 'subscription:' : 'file:') + p.name;
        var isActive = ref === active;
        return '<tr><td>' + esc(p.name) + '</td>' +
          '<td>' + (p.type === 'subscription' ? '订阅' : '文件') + '</td>' +
          '<td>' + esc(p.updated || '') + '</td>' +
          '<td>' + (isActive ? '<b class="ok">活动</b>' : (p.has_file ? '' : '未下载')) + '</td>' +
          '<td>' +
          (isActive ? '' : '<button class="mini" data-act="activate" data-ref="' + esc(ref) + '">设为活动</button>') +
          (p.type === 'file' ? '<button class="mini" data-act="del" data-name="' + esc(p.name) + '">删除</button>' : '') +
          '</td></tr>';
      }).join('');
      tbody.querySelectorAll('button').forEach(function (b) {
        b.addEventListener('click', function () {
          if (b.getAttribute('data-act') === 'del') {
            var name = b.getAttribute('data-name');
            if (!confirm('确定删除配置文件「' + name + '」吗？')) return;
            showMsg('正在删除……');
            api('/api/profiles?name=' + encodeURIComponent(name), 'DELETE').then(function (r) {
              showMsg(r.ok ? '已删除' : '失败：' + (r.error || '未知错误'), !r.ok);
              loadProfiles();
            });
          } else {
            var ref = b.getAttribute('data-ref');
            showMsg('正在切换配置文件……');
            api('/api/profiles/active', 'POST', { ref: ref }).then(function (r) {
              showMsg(r.ok ? '已切换并重启' : '失败：' + (r.error || '未知错误'), !r.ok);
              loadProfiles();
              loadAppSettings();
            });
          }
        });
      });
    }).catch(function () { showMsg('配置文件列表加载失败', true); });
  }
  $('profile-upload-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var file = $('profile-file').files[0];
    if (!file) { showMsg('请先选择一个 YAML 文件', true); return; }
    showMsg('正在上传「' + file.name + '」……');
    var fd = new FormData();
    fd.append('file', file);
    fetch('/api/profiles/upload', { method: 'POST', body: fd }).then(function (res) { return res.json(); }).then(function (r) {
      showMsg(r.ok ? '已上传为「' + r.name + '」' : '失败：' + (r.error || '未知错误'), !r.ok);
      if (r.ok) $('profile-file').value = '';
      loadProfiles();
    }).catch(function () { showMsg('上传失败', true); });
  });
  // 正在编辑的订阅名称（空 = 新增模式）；编辑时名称锁定，只改其余设置
  var editingName = '';
  // 间隔显示：0 = 不自动；0.5 = 30 分钟；1 = 1 小时；1.5 = 1.5 小时
  function formatInterval(h) {
    if (!h || h <= 0) return '不自动';
    if (h < 1) return Math.round(h * 60) + ' 分钟';
    return (h % 1 === 0 ? h : h) + ' 小时';
  }
  function exitSubEdit() {
    editingName = '';
    $('sub-name').disabled = false;
    $('sub-name').value = '';
    $('sub-url').value = '';
    $('sub-ua').value = '';
    $('sub-interval').value = '0';
    $('sub-proxy').checked = false;
    $('btn-sub-save').textContent = '添加 / 更新';
    $('btn-sub-cancel').style.display = 'none';
  }
  $('btn-sub-cancel').addEventListener('click', function () { exitSubEdit(); showMsg('已取消编辑'); });
  function loadSubs() {
    api('/api/subscriptions').then(function (d) {
      var tbody = document.querySelector('#sub-table tbody');
      var list = (d && d.subscriptions) || [];
      if (!list.length) {
        tbody.innerHTML = '<tr><td colspan="7">暂无订阅</td></tr>';
        return;
      }
      tbody.innerHTML = list.map(function (s) {
        var interval = formatInterval(s.interval_hours);
        return '<tr><td>' + esc(s.name) + '</td>' +
          '<td class="url" title="' + esc(s.url) + '">' + esc(s.url) + '</td>' +
          '<td>' + esc(s.user_agent || '') + '</td>' +
          '<td>' + interval + '</td>' +
          '<td>' + (s.use_proxy ? '是' : '否') + '</td>' +
          '<td>' + esc(s.updated || '') + '</td>' +
          '<td><button class="mini" data-act="edit" data-name="' + esc(s.name) + '" data-url="' + esc(s.url) + '" data-ua="' + esc(s.user_agent || '') + '" data-interval="' + (s.interval_hours || 0) + '" data-proxy="' + (s.use_proxy ? '1' : '0') + '">编辑</button>' +
          '<button class="mini" data-act="update" data-name="' + esc(s.name) + '" data-url="' + esc(s.url) + '" data-ua="' + esc(s.user_agent || '') + '" data-proxy="' + (s.use_proxy ? '1' : '0') + '">更新</button>' +
          '<button class="mini" data-act="del" data-name="' + esc(s.name) + '">删除</button></td></tr>';
      }).join('');
      tbody.querySelectorAll('button').forEach(function (b) {
        b.addEventListener('click', function () {
          var name = b.getAttribute('data-name');
          if (b.getAttribute('data-act') === 'edit') {
            // 把这条订阅填回表单改：名称是标识，改的时候锁定，只改链接/间隔/VPN
            editingName = name;
            $('sub-name').value = name;
            $('sub-name').disabled = true;
            $('sub-url').value = b.getAttribute('data-url');
            $('sub-ua').value = b.getAttribute('data-ua');
            $('sub-interval').value = b.getAttribute('data-interval') || '0';
            $('sub-proxy').checked = b.getAttribute('data-proxy') === '1';
            $('btn-sub-save').textContent = '保存修改';
            $('btn-sub-cancel').style.display = '';
            showMsg('正在编辑「' + name + '」，改好后点“保存修改”。');
            return;
          }
          if (b.getAttribute('data-act') === 'del') {
            if (!confirm('确定删除订阅「' + name + '」吗？对应的配置文件也会删除。')) return;
            showMsg('正在删除……');
            api('/api/subscriptions?name=' + encodeURIComponent(name), 'DELETE').then(function (r) {
              showMsg(r.ok ? '已删除' : '失败：' + (r.error || '未知错误'), !r.ok);
              loadSubs(); loadProfiles();
            });
          } else {
            showMsg('正在更新「' + name + '」……');
            api('/api/subscriptions', 'POST', { name: name, url: b.getAttribute('data-url'), user_agent: b.getAttribute('data-ua'), use_proxy: b.getAttribute('data-proxy') === '1' }).then(function (r) {
              var msg = r.ok ? '已更新' + (r.count ? '：' + r.count + ' 个节点' : '') : '失败：' + (r.error || '未知错误');
              if (r.ok && r.warning) msg += '（内容拉取失败：' + r.warning + '）';
              showMsg(msg, !r.ok);
              loadSubs(); loadProfiles();
            });
          }
        });
      });
    }).catch(function () { showMsg('订阅列表加载失败', true); });
  }
  $('sub-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var name = $('sub-name').value.trim();
    var url = $('sub-url').value.trim();
    var ua = $('sub-ua').value.trim();
    var interval = parseFloat($('sub-interval').value) || 0;
    if (interval < 0) interval = 0;
    if (interval > 0 && interval < 0.5) interval = 0.5;
    var useProxy = $('sub-proxy').checked;
    if (!name || !url) { showMsg('请先填写名称和订阅链接', true); return; }
    showMsg(editingName ? '正在保存修改……' : '正在拉取订阅……');
    api('/api/subscriptions', 'POST', { name: name, url: url, user_agent: ua, interval_hours: interval, use_proxy: useProxy }).then(function (r) {
      var msg = r.ok ? (editingName ? '已保存修改' : '已添加/更新') + (r.count ? '：' + r.count + ' 个节点' : '') : '失败：' + (r.error || '未知错误');
      if (r.ok && r.warning) msg += '（设置已保存，但订阅内容拉取失败：' + r.warning + '）';
      showMsg(msg, !r.ok);
      if (r.ok) exitSubEdit();
      loadSubs(); loadProfiles();
    }).catch(function () { showMsg('请求失败', true); });
  });
  $('btn-update-all').addEventListener('click', function () {
    showMsg('正在更新全部订阅……');
    api('/api/subscriptions/update', 'POST', {}).then(function (r) {
      var extra = (r.failed && r.failed.length) ? '，失败：' + r.failed.map(function (f) { return f.name; }).join('、') : '';
      showMsg(r.ok ? '更新完成，共更新 ' + r.total + ' 个订阅' + extra : '失败：' + (r.error || '未知错误'), !r.ok);
      loadSubs(); loadProfiles();
    }).catch(function () { showMsg('请求失败', true); });
  });
  $('btn-refresh-subs').addEventListener('click', loadSubs);

  // ---- mixin ----
  function tri(id) {
    var v = $(id).value;
    if (v === '') return null;
    return v === '1';
  }
  function setTri(id, v) {
    $(id).value = v == null ? '' : (v ? '1' : '0');
  }
  function num(id) {
    var v = $(id).value.trim();
    if (v === '') return null;
    var n = parseInt(v, 10);
    return isNaN(n) ? null : n;
  }
  function setNum(id, v) {
    $(id).value = v == null ? '' : v;
  }
  function list(id) {
    var lines = $(id).value.split('\n').map(function (s) { return s.trim(); }).filter(function (s) { return s !== ''; });
    return lines.length ? lines : null;
  }
  function setList(id, arr) {
    $(id).value = arr && arr.length ? arr.join('\n') : '';
  }
  // 官方（Nikki）默认配置：混入里没填的项按这个填上，可自行修改。
  var mixinDefaults = {
    log_level: 'warning', mode: 'rule', ipv6: true, allow_lan: true,
    mixed_port: 7890, http_port: 8080, socks_port: 1080, redir_port: 7891, tproxy_port: 7892,
    api_listen: '0.0.0.0:9090',
    dns_enabled: true, dns_listen: '[::]:1053', dns_mode: 'fake-ip',
    fake_ip_range: '198.18.0.1/16', nameserver: ['223.5.5.5', '223.6.6.6'], default_nameserver: ['223.5.5.5'],
    tun_enabled: true, tun_device: 'nikki', tun_stack: 'mixed'
  };
  function loadMixin() {
    api('/api/mixin').then(function (m) {
      m = m || {};
      var d = mixinDefaults;
      $('mx-log-level').value = m.log_level || d.log_level;
      $('mx-mode').value = m.mode || d.mode;
      setTri('mx-ipv6', m.ipv6 == null ? d.ipv6 : m.ipv6);
      setTri('mx-allow-lan', m.allow_lan == null ? d.allow_lan : m.allow_lan);
      setNum('mx-mixed-port', m.mixed_port == null ? d.mixed_port : m.mixed_port);
      setNum('mx-http-port', m.http_port == null ? d.http_port : m.http_port);
      setNum('mx-socks-port', m.socks_port == null ? d.socks_port : m.socks_port);
      setNum('mx-redir-port', m.redir_port == null ? d.redir_port : m.redir_port);
      setNum('mx-tproxy-port', m.tproxy_port == null ? d.tproxy_port : m.tproxy_port);
      $('mx-api-listen').value = m.api_listen || d.api_listen;
      setTri('mx-selection-cache', m.selection_cache);
      setTri('mx-dns-enabled', m.dns_enabled == null ? d.dns_enabled : m.dns_enabled);
      $('mx-dns-listen').value = m.dns_listen || d.dns_listen;
      $('mx-dns-mode').value = m.dns_mode || d.dns_mode;
      $('mx-fake-ip-range').value = m.fake_ip_range || d.fake_ip_range;
      setList('mx-nameserver', (m.nameserver && m.nameserver.length) ? m.nameserver : d.nameserver);
      setList('mx-default-nameserver', (m.default_nameserver && m.default_nameserver.length) ? m.default_nameserver : d.default_nameserver);
      setTri('mx-tun-enabled', m.tun_enabled == null ? d.tun_enabled : m.tun_enabled);
      $('mx-tun-device').value = m.tun_device || d.tun_device;
      $('mx-tun-stack').value = m.tun_stack || d.tun_stack;
      setNum('mx-tun-mtu', m.tun_mtu);
    }).catch(function () { showMsg('混入配置加载失败', true); });
  }
  $('mixin-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var body = {};
    function putStr(k, v) { if (v !== '') body[k] = v; }
    function putTri(k, v) { if (v !== null) body[k] = v; }
    function putNum(k, v) { if (v !== null) body[k] = v; }
    function putList(k, v) { if (v !== null) body[k] = v; }
    putStr('log_level', $('mx-log-level').value);
    putStr('mode', $('mx-mode').value);
    putTri('ipv6', tri('mx-ipv6'));
    putTri('allow_lan', tri('mx-allow-lan'));
    putNum('mixed_port', num('mx-mixed-port'));
    putNum('http_port', num('mx-http-port'));
    putNum('socks_port', num('mx-socks-port'));
    putNum('redir_port', num('mx-redir-port'));
    putNum('tproxy_port', num('mx-tproxy-port'));
    putStr('api_listen', $('mx-api-listen').value.trim());
    putTri('selection_cache', tri('mx-selection-cache'));
    putTri('dns_enabled', tri('mx-dns-enabled'));
    putStr('dns_listen', $('mx-dns-listen').value.trim());
    putStr('dns_mode', $('mx-dns-mode').value);
    putStr('fake_ip_range', $('mx-fake-ip-range').value.trim());
    putList('nameserver', list('mx-nameserver'));
    putList('default_nameserver', list('mx-default-nameserver'));
    putTri('tun_enabled', tri('mx-tun-enabled'));
    putStr('tun_device', $('mx-tun-device').value.trim());
    putStr('tun_stack', $('mx-tun-stack').value);
    putNum('tun_mtu', num('mx-tun-mtu'));
    showMsg('正在保存混入配置并重启……');
    api('/api/mixin', 'PUT', body).then(function (r) {
      showMsg(r.ok ? '已保存，Mihomo 已重启' : '失败：' + (r.error || '未知错误'), !r.ok);
    }).catch(function () { showMsg('请求失败', true); });
  });
  function loadMixinFile() {
    api('/api/mixin-file').then(function (d) {
      $('mixin-file-text').value = d.yaml || '';
    }).catch(function () { showMsg('mixin.yaml 加载失败', true); });
  }
  $('btn-mixin-file-save').addEventListener('click', function () {
    showMsg('正在保存 mixin.yaml 并重启……');
    api('/api/mixin-file', 'PUT', { yaml: $('mixin-file-text').value }).then(function (r) {
      showMsg(r.ok ? '已保存，Mihomo 已重启' : '失败：' + (r.error || '未知错误'), !r.ok);
    }).catch(function () { showMsg('请求失败', true); });
  });
  $('btn-mixin-file-reload').addEventListener('click', loadMixinFile);

  // ---- proxy: 运行模式 + 代理组 ----
  var MODE_NAMES = { rule: '规则模式', global: '全局模式', direct: '直连模式' };
  function loadMode() {
    api('/api/mode').then(function (d) {
      var mode = d.mode || 'rule';
      $('mode-now').textContent = MODE_NAMES[mode] || mode;
      document.querySelectorAll('#mode-btns button').forEach(function (b) {
        b.classList.toggle('active', b.getAttribute('data-mode') === mode);
      });
    }).catch(function () { $('mode-now').textContent = '未知'; });
  }
  document.querySelectorAll('#mode-btns button').forEach(function (b) {
    b.addEventListener('click', function () {
      var mode = b.getAttribute('data-mode');
      showMsg('正在切换到' + (MODE_NAMES[mode] || mode) + '……');
      api('/api/mode', 'PUT', { mode: mode }).then(function (r) {
        showMsg(r.ok ? '已切换到' + (MODE_NAMES[mode] || mode) : '失败：' + (r.error || '未知错误'), !r.ok);
        loadMode(); loadStatus();
      }).catch(function () { showMsg('请求失败', true); });
    });
  });
  // 代理组：只显示选中的一个组（下拉框切换），节点多时用搜索框筛选；
  // 全部组全部节点都展开会把页面拉得非常长，选节点请用下拉框或下方实时面板。
  var cachedProxyGroups = {};
  var selectedProxyGroup = 'GLOBAL';
  function proxyGroupNames() {
    return Object.keys(cachedProxyGroups).filter(function (n) {
      var g = cachedProxyGroups[n];
      return g && g.all && g.all.length > 1;
    });
  }
  // 节点延迟（毫秒），-1 = 超时/不通；按组缓存，切换组或点测速时刷新。
  var nodeDelays = {};
  var delayTesting = false;
  function delayLabel(n) {
    if (!(n in nodeDelays)) return '';
    var d = nodeDelays[n];
    if (d < 0) return ' <span class="delay bad">超时</span>';
    var cls = d < 300 ? 'ok' : (d < 1000 ? 'warn' : 'bad');
    return ' <span class="delay ' + cls + '">' + d + 'ms</span>';
  }
  function testNodeDelays() {
    if (delayTesting || !selectedProxyGroup) return;
    delayTesting = true;
    var btn = $('btn-proxy-delay');
    if (btn) btn.disabled = true;
    api('/api/proxies/delay?group=' + encodeURIComponent(selectedProxyGroup)).then(function (d) {
      delayTesting = false;
      if (btn) btn.disabled = false;
      if (d && d.ok && d.delays) {
        nodeDelays = d.delays;
        renderProxyGroup();
      }
    }).catch(function () {
      delayTesting = false;
      if (btn) btn.disabled = false;
    });
  }
  function renderProxyGroup() {
    var box = $('proxy-groups');
    var sel = $('proxy-group-select');
    var names = proxyGroupNames();
    if (!names.length) {
      sel.innerHTML = '';
      box.innerHTML = '<p class="note">暂无代理组（Mihomo 未运行，或当前配置里没有可选节点的组）。</p>';
      return;
    }
    if (names.indexOf(selectedProxyGroup) === -1) selectedProxyGroup = names[0];
    sel.innerHTML = names.map(function (n) {
      return '<option value="' + esc(n) + '"' + (n === selectedProxyGroup ? ' selected' : '') + '>' + esc(n) + '</option>';
    }).join('');
    var g = cachedProxyGroups[selectedProxyGroup];
    var now = g.now || '';
    var filter = ($('proxy-node-filter').value || '').trim().toLowerCase();
    var all = g.all || [];
    var nodes = all.filter(function (n) {
      return !filter || n.toLowerCase().indexOf(filter) !== -1;
    });
    // 节点按延迟从小到大排：有延迟的按毫秒升序，超时的排后面，未测速的排最后。
    nodes.sort(function (a, b) {
      var da = a in nodeDelays ? nodeDelays[a] : -2;
      var db = b in nodeDelays ? nodeDelays[b] : -2;
      var ra = da < 0 ? (da === -1 ? 1 : 2) : 0;
      var rb = db < 0 ? (db === -1 ? 1 : 2) : 0;
      if (ra !== rb) return ra - rb;
      if (ra === 0) return da - db;
      return 0;
    });
    var html = '<p>当前：<b>' + esc(now) + '</b>（共 ' + all.length + ' 个节点' + (filter ? '，已筛选出 ' + nodes.length + ' 个' : '') + '）</p>';
    if (!nodes.length) {
      html += '<p class="note">没有匹配的节点。</p>';
    } else {
      html += '<div class="row">' + nodes.map(function (n) {
        var cls = n === now ? ' class="active"' : '';
        return '<button data-group="' + esc(selectedProxyGroup) + '" data-node="' + esc(n) + '"' + cls + '>' + esc(n) + delayLabel(n) + '</button>';
      }).join('') + '</div>';
    }
    box.innerHTML = html;
    box.querySelectorAll('button[data-group]').forEach(function (b) {
      b.addEventListener('click', function () {
        var group = b.getAttribute('data-group');
        var node = b.getAttribute('data-node');
        showMsg('正在把「' + group + '」切换到「' + node + '」……');
        api('/api/proxies/' + encodeURIComponent(group), 'PUT', { name: node }).then(function (r) {
          showMsg(r.ok ? '「' + group + '」已切换到「' + node + '」' : '失败：' + (r.error || '未知错误'), !r.ok);
          loadProxyGroups();
        }).catch(function () { showMsg('请求失败', true); });
      });
    });
  }
  function loadProxyGroups() {
    api('/api/proxies').then(function (d) {
      cachedProxyGroups = (d && d.proxies) || {};
      renderProxyGroup();
      testNodeDelays(); // 加载完自动测一次延迟
    }).catch(function () { $('proxy-groups').innerHTML = '<p class="note">代理组加载失败</p>'; });
  }
  $('proxy-group-select').addEventListener('change', function () {
    selectedProxyGroup = $('proxy-group-select').value;
    nodeDelays = {}; // 换组后旧延迟作废
    renderProxyGroup();
    testNodeDelays();
  });
  $('proxy-node-filter').addEventListener('input', renderProxyGroup);
  $('btn-proxy-delay').addEventListener('click', testNodeDelays);
  $('btn-proxy-refresh').addEventListener('click', function () { loadMode(); loadProxyGroups(); });
  function ensureLiveFrame() {
    var frame = $('live-frame');
    // 带上版本号做缓存戳：升级后地址变了，浏览器就不会再用缓存的旧面板 JS
    // （Ctrl+F5 在飞牛桌面里会跳回应用页，所以这里必须自己防缓存）。
    var stamp = appVersion || Date.now();
    var url = 'http://' + window.location.hostname + ':' + ctlPort + '/ui/?v=' + encodeURIComponent(stamp);
    if (frame.getAttribute('src') !== url) {
      frame.setAttribute('src', url);
    } else {
      // 地址没变时浏览器不会重新加载：之前核心停着时那次失败的加载会
      // 一直留着。强制刷新一次，让核心起来后面板能重新打开。
      try { frame.contentWindow.location.reload(); } catch (e) {}
    }
  }

  // ---- editor ----
  var edRef = '';
  function loadEditor(ref) {
    var url = '/api/config' + (ref ? '?ref=' + encodeURIComponent(ref) : '');
    api(url).then(function (d) {
      edRef = d.ref || '';
      $('ed-ref').textContent = d.ref || '—';
      $('ed-text').value = d.yaml || '';
      if ($('ed-profile').value !== edRef) $('ed-profile').value = edRef;
      if (d.ok === false && d.error) showMsg('加载失败：' + d.error, true);
    }).catch(function () { showMsg('配置文件加载失败', true); });
  }
  function loadEditorProfiles() {
    api('/api/profiles').then(function (d) {
      var sel = $('ed-profile');
      var list = (d && d.profiles) || [];
      var active = d.active || '';
      var cur = sel.value || edRef || active;
      sel.innerHTML = list.map(function (p) {
        var ref = (p.type === 'subscription' ? 'subscription:' : 'file:') + p.name;
        return '<option value="' + esc(ref) + '">' + esc(ref) + (ref === active ? '（当前使用）' : '') + '</option>';
      }).join('');
      if (cur && list.some(function (p) { return ((p.type === 'subscription' ? 'subscription:' : 'file:') + p.name) === cur; })) {
        sel.value = cur;
      } else if (active) {
        sel.value = active;
      }
      edRef = sel.value;
    });
  }
  $('ed-profile').addEventListener('change', function () {
    loadEditor($('ed-profile').value);
  });
  $('btn-ed-save').addEventListener('click', function () {
    if (!confirm('保存后将重新生成运行配置并重启 Mihomo，确定吗？')) return;
    showMsg('正在保存并重启……');
    api('/api/config', 'PUT', { yaml: $('ed-text').value, ref: edRef }).then(function (r) {
      showMsg(r.ok ? '已保存，Mihomo 已重启' : '失败：' + (r.error || '未知错误'), !r.ok);
    }).catch(function () { showMsg('请求失败', true); });
  });
  $('btn-ed-reload').addEventListener('click', loadEditor);

  // ---- logs ----
  // ---- 日志中文解释 ----
  // 每行日志后面追加一句中文说明：现在在做什么、遇到什么问题。
  // 只做展示层翻译，原始日志文件不动。
  var LOG_NOTES = [
    // 控制台 / 启动脚本
    [/Starting console backend \.\.\./, '正在启动控制台后端'],
    [/backend pid = (\d+)/, '控制台后端已启动，进程号 $1'],
    [/service disabled, skip starting mihomo/, '服务未启用，已跳过启动 Mihomo（请在控制台勾选“启用服务”）'],
    [/Starting mihomo \.\.\./, '正在启动 Mihomo'],
    [/Stopping mihomo \.\.\./, '正在停止 Mihomo'],
    [/send TERM signal to backend PID: (\d+)/, '正在向控制台后端进程 $1 发送停止信号'],
    [/send TERM signal to PID: (\d+)/, '正在向 Mihomo 进程 $1 发送停止信号'],
    [/mihomo started pid=(\d+)/, 'Mihomo 已启动，进程号 $1'],
    [/mihomo pid = (\d+)/, 'Mihomo 进程号 $1'],
    [/waiting process terminal\.\.\. \((\d+)s\/(\d+)s\)/, '等待进程退出（已等 $1 秒，最多等 $2 秒）'],
    [/DjMihomo console ([\d.]+) listening on ([^\s]+)/, 'DjMihomo 控制台 $1 已启动，监听 $2'],
    [/default config installed/, '已安装默认配置'],
    [/UI installed/, '已安装实时面板'],
    [/^pid=(\d+)$/, '进程号 $1'],
    // Mihomo 核心（logrus 格式）
    [/msg="Mihomo shutting down"/, 'Mihomo 正在关闭'],
    [/msg="Start initial configuration in progress"/, '正在加载初始配置'],
    [/msg="Initial configuration complete"/, '初始配置加载完成'],
    [/msg="Geodata Loader mode: memconservative"/, '地理数据库加载模式：节省内存'],
    [/msg="Geosite Matcher implementation: succinct"/, '域名规则匹配器已就绪'],
    [/msg="Can't find (MMDB|GeoIP)(\.dat)?/, '找不到 IP 地理数据库，正在下载'],
    [/msg="Can't find GeoSite(\.dat)?/, '找不到域名规则数据库，正在下载'],
    [/msg="Use new GeoSite database"/, '已使用新的域名规则数据库'],
    [/msg="Use new MMDB database"/, '已使用新的 IP 地理数据库'],
    [/msg="Start listener ([^"]*)"/, '正在启动监听：$1'],
    [/msg="Start to listen ([^"]*)"/, '开始监听：$1'],
    [/msg="configuration complete"/, '配置加载完成'],
    [/msg="Start initial configuration in progress"/, '正在加载初始配置'],
    [/\[Provider\] (.+) \/ (Domain|IP) pull error: (\d+) (.+)"/, '规则集 $1 拉取失败（$3 $4），源站可能屏蔽了本次请求，稍后重试或检查订阅地址'],
    [/\[Provider\] (.+) \/ (Domain|IP) pull error: (.+)"/, '规则集 $1 拉取失败：$3'],
    [/level=error/, '出错了，请看本行内容'],
    [/level=warning/, '警告，可留意本行内容']
  ];
  function translateLog(text) {
    if (!text) return text;
    // Newest first: reverse the lines so the latest entry is on top.
    return text.split('\n').reverse().map(function (line) {
      for (var i = 0; i < LOG_NOTES.length; i++) {
        var m = line.match(LOG_NOTES[i][0]);
        if (m) {
          var note = LOG_NOTES[i][1];
          for (var j = 1; j < m.length; j++) {
            note = note.split('$' + j).join(m[j]);
          }
          return line + '  ← ' + note;
        }
      }
      return line;
    }).join('\n');
  }
  // 日志切换：默认显示应用日志，点“核心日志”切到核心日志，可来回切换
  var currentLog = 'app';
  var cachedLogs = { core: '', app: '' };
  function showLog() {
    $('btn-log-app').classList.toggle('active', currentLog === 'app');
    $('btn-log-core').classList.toggle('active', currentLog === 'core');
    var text = currentLog === 'app' ? cachedLogs.app : cachedLogs.core;
    $('log-pre').textContent = text || '(暂无日志)';
  }
  $('btn-log-app').addEventListener('click', function () { currentLog = 'app'; showLog(); });
  $('btn-log-core').addEventListener('click', function () { currentLog = 'core'; showLog(); });
  function loadLogs() {
    api('/api/logs?lines=30').then(function (d) {
      cachedLogs.core = translateLog(d.lines);
      cachedLogs.app = translateLog(d.backend);
      showLog();
    }).catch(function () { $('log-pre').textContent = '日志加载失败'; });
  }
  $('btn-log-refresh').addEventListener('click', loadLogs);
  // 复制当前显示的日志，方便粘贴发给 AI 问如何解决
  $('btn-log-copy').addEventListener('click', function () {
    var text = $('log-pre').textContent;
    if (!text || text === '(暂无日志)') { showMsg('没有可复制的日志', true); return; }
    function done() { showMsg('日志已复制，可粘贴发给 AI'); }
    function fail() { showMsg('复制失败，请手动选中复制', true); }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, fail);
    } else {
      var range = document.createRange();
      range.selectNodeContents($('log-pre'));
      var sel = window.getSelection();
      sel.removeAllRanges();
      sel.addRange(range);
      try { document.execCommand('copy'); done(); } catch (e) { fail(); }
      sel.removeAllRanges();
    }
  });
  // 只清空当前正在看的那个日志，不动另一个
  $('btn-log-clear').addEventListener('click', function () {
    var name = currentLog === 'app' ? '应用日志' : '核心日志';
    if (!confirm('确定清空' + name + '吗？（另一个日志不受影响）')) return;
    api('/api/logs/clear', 'POST', { which: currentLog }).then(function (r) {
      showMsg(r.ok ? name + '已清空' : '失败', !r.ok);
      loadLogs();
    }).catch(function () { showMsg('请求失败', true); });
  });

  loadStatus();
  loadAppSettings();
  // 刷新后恢复上次的菜单
  try {
    var savedTab = localStorage.getItem('djmihomo-tab');
    var validTabs = ['app', 'profiles', 'mixin', 'proxy', 'editor', 'live', 'logs'];
    if (savedTab && validTabs.indexOf(savedTab) !== -1 && savedTab !== 'app') {
      switchTab(savedTab);
    }
  } catch (e) {}
})();
