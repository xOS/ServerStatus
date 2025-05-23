let LANG = {
  Add: "添加",
  Edit: "修改",
  AlarmRule: "报警规则",
  Notification: "通知方式",
  Server: "服务器",
  Monitor: "监控",
  Traffic: "流量",
  Cron: "计划任务",
}

// 标记流量相关函数已在主JS文件中准备就绪
window.trafficFunctionsReady = true;

function updateLang(newLang) {
  if (newLang) {
    LANG = newLang;
  }
}

function readableBytes(bytes) {
  if (!bytes) {
    return '0B'
  }
  var i = Math.floor(Math.log(bytes) / Math.log(1024)),
    sizes = ["B", "KB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"];
  const value = parseFloat((bytes / Math.pow(1024, i)).toFixed(2));
  return `${value}${sizes[i]}`;
}

const confirmBtn = $(".mini.confirm.modal .server-primary-btn.button");

function showConfirm(title, content, callFn, extData) {
  const modal = $(".mini.confirm.modal");
  modal.children(".header").text(title);
  modal.children(".content").text(content);
  if (confirmBtn.hasClass("loading")) {
    return false;
  }
  modal
    .modal({
      closable: true,
      onApprove: function () {
        confirmBtn.toggleClass("loading");
        callFn(extData);
        return false;
      },
    })
    .modal("show");
}

function postJson(url, data) {
  return $.ajax({
    url: url,
    type: "POST",
    contentType: "application/json",
    data: JSON.stringify(data),
  }).done((resp) => {
    if (resp.code == 200) {
      if (resp.message) {
        alert(resp.message);
      } else {
        alert("删除成功");
      }
      window.location.reload();
    } else {
      alert("删除失败 " + resp.code + "：" + resp.message);
      confirmBtn.toggleClass("loading");
    }
  })
    .fail((err) => {
      alert("网络错误：" + err.responseText);
    });
}

function showFormModal(modelSelector, formID, URL, getData) {
  $(modelSelector)
    .modal({
      closable: true,
      onApprove: function () {
        let success = false;
        const btn = $(modelSelector + " .server-primary-btn.button");
        const form = $(modelSelector + " form");
        if (btn.hasClass("loading")) {
          return success;
        }
        form.children(".message").remove();
        btn.toggleClass("loading");
        const data = getData
          ? getData()
          : $(formID)
            .serializeArray()
            .reduce(function (obj, item) {
              // ID 类的数据
              if (
                item.name.endsWith("_id") ||
                item.name === "id" ||
                item.name === "ID" ||
                item.name === "ServerID" ||
                item.name === "RequestType" ||
                item.name === "RequestMethod" ||
                item.name === "TriggerMode" ||
                item.name === "TaskType" ||
                item.name === "DisplayIndex" ||
                item.name === "Type" ||
                item.name === "Cover" ||
                item.name === "Duration" ||
                item.name === "MaxRetries" ||
                item.name === "Provider" ||
                item.name === "WebhookMethod" ||
                item.name === "WebhookRequestType"
              ) {
                obj[item.name] = parseInt(item.value);
              } else if (item.name.endsWith("Latency")) {
                obj[item.name] = parseFloat(item.value);
              } else {
                obj[item.name] = item.value;
              }

              if (item.name.endsWith("ServersRaw")) {
                if (item.value.length > 2) {
                  obj[item.name] = JSON.stringify(
                    [...item.value.matchAll(/\d+/gm)].map((k) =>
                      parseInt(k[0])
                    )
                  );
                }
              }

              if (item.name.endsWith("TasksRaw")) {
                if (item.value.length > 2) {
                  obj[item.name] = JSON.stringify(
                    [...item.value.matchAll(/\d+/gm)].map((k) =>
                      parseInt(k[0])
                    )
                  );
                }
              }

              if (item.name.endsWith("DDNSProfilesRaw")) {
                if (item.value.length > 2) {
                  obj[item.name] = JSON.stringify(
                    [...item.value.matchAll(/\d+/gm)].map((k) =>
                      parseInt(k[0])
                    )
                  );
                }
              }

              return obj;
            }, {});
        $.post(URL, JSON.stringify(data))
          .done(function (resp) {
            if (resp.code == 200) {
              window.location.reload()
            } else {
              form.append(
                `<div class="ui negative message"><div class="header">操作失败</div><p>` +
                resp.message +
                `</p></div>`
              );
            }
          })
          .fail(function (err) {
            form.append(
              `<div class="ui negative message"><div class="header">网络错误</div><p>` +
              err.responseText +
              `</p></div>`
            );
          })
          .always(function () {
            btn.toggleClass("loading");
          });
        return success;
      },
    })
    .modal("show");
}

function addOrEditAlertRule(rule) {
  const modal = $(".rule.modal");
  modal.children(".header").text((rule ? LANG.Edit : LANG.Add) + ' ' + LANG.AlarmRule);
  modal
    .find(".server-primary-btn.button")
    .html(
      rule ? LANG.Edit + '<i class="edit icon"></i>' : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("input[name=ID]").val(rule ? rule.ID : null);
  modal.find("input[name=Name]").val(rule ? rule.Name : null);
  modal.find("textarea[name=RulesRaw]").val(rule ? rule.RulesRaw : null);
  modal.find("select[name=TriggerMode]").val(rule ? rule.TriggerMode : 0);
  modal.find("input[name=NotificationTag]").val(rule ? rule.NotificationTag : null);
  if (rule && rule.Enable) {
    modal.find(".ui.rule-enable.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.rule-enable.checkbox").checkbox("set unchecked");
  }
  modal.find("a.ui.label.visible").each((i, el) => {
    el.remove();
  });
  var failTriggerTasks;
  var recoverTriggerTasks;
  if (rule) {
    failTriggerTasks = rule.FailTriggerTasksRaw;
    recoverTriggerTasks = rule.RecoverTriggerTasksRaw;
    const failTriggerTasksList = JSON.parse(failTriggerTasks || "[]");
    const recoverTriggerTasksList = JSON.parse(recoverTriggerTasks || "[]");
    const node1 = modal.find("i.dropdown.icon.1");
    const node2 = modal.find("i.dropdown.icon.2");
    for (let i = 0; i < failTriggerTasksList.length; i++) {
      node1.after(
        '<a class="ui label transition visible" data-value="' +
        failTriggerTasksList[i] +
        '" style="display: inline-block !important;">ID:' +
        failTriggerTasksList[i] +
        '<i class="delete icon"></i></a>'
      );
    }
    for (let i = 0; i < recoverTriggerTasksList.length; i++) {
      node2.after(
        '<a class="ui label transition visible" data-value="' +
        recoverTriggerTasksList[i] +
        '" style="display: inline-block !important;">ID:' +
        recoverTriggerTasksList[i] +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  modal
    .find("input[name=FailTriggerTasksRaw]")
    .val(rule ? "[]," + failTriggerTasks.substr(1, failTriggerTasks.length - 2) : "[]");
  modal
    .find("input[name=RecoverTriggerTasksRaw]")
    .val(rule ? "[]," + recoverTriggerTasks.substr(1, recoverTriggerTasks.length - 2) : "[]");

  showFormModal(".rule.modal", "#ruleForm", "/api/alert-rule");
}

function addOrEditNotification(notification) {
  const modal = $(".notification.modal");
  modal.children(".header").text((notification ? LANG.Edit : LANG.Add) + ' ' + LANG.Notification);
  modal
    .find(".server-primary-btn.button")
    .html(
      notification
        ? LANG.Edit + '<i class="edit icon"></i>'
        : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("input[name=ID]").val(notification ? notification.ID : null);
  modal.find("input[name=Name]").val(notification ? notification.Name : null);
  modal.find("input[name=Tag]").val(notification ? notification.Tag : null);
  modal.find("input[name=URL]").val(notification ? notification.URL : null);
  modal
    .find("textarea[name=RequestHeader]")
    .val(notification ? notification.RequestHeader : null);
  modal
    .find("textarea[name=RequestBody]")
    .val(notification ? notification.RequestBody : null);
  modal
    .find("select[name=RequestMethod]")
    .val(notification ? notification.RequestMethod : 1);
  modal
    .find("select[name=RequestType]")
    .val(notification ? notification.RequestType : 1);
  if (notification && notification.VerifySSL) {
    modal.find(".ui.nf-ssl.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.nf-ssl.checkbox").checkbox("set unchecked");
  }
  modal.find(".ui.nf-skip-check.checkbox").checkbox("set unchecked");
  showFormModal(
    ".notification.modal",
    "#notificationForm",
    "/api/notification"
  );
}

function addOrEditDDNS(ddns) {
  const modal = $(".ddns.modal");
  modal.children(".header").text((ddns ? LANG.Edit : LANG.Add));
  modal
    .find(".server-primary-btn.button")
    .html(
      ddns
        ? LANG.Edit + '<i class="edit icon"></i>'
        : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("input[name=ID]").val(ddns ? ddns.ID : null);
  modal.find("input[name=Name]").val(ddns ? ddns.Name : null);
  modal.find("input[name=DomainsRaw]").val(ddns ? ddns.DomainsRaw : null);
  modal.find("input[name=AccessID]").val(ddns ? ddns.AccessID : null);
  modal.find("input[name=AccessSecret]").val(ddns ? ddns.AccessSecret : null);
  modal.find("input[name=MaxRetries]").val(ddns ? ddns.MaxRetries : 3);
  modal.find("input[name=WebhookURL]").val(ddns ? ddns.WebhookURL : null);
  modal
    .find("textarea[name=WebhookHeaders]")
    .val(ddns ? ddns.WebhookHeaders : null);
  modal
    .find("textarea[name=WebhookRequestBody]")
    .val(ddns ? ddns.WebhookRequestBody : null);
  modal
    .find("select[name=Provider]")
    .val(ddns ? ddns.Provider : 0);
  modal
    .find("select[name=WebhookMethod]")
    .val(ddns ? ddns.WebhookMethod : 1);
  modal
    .find("select[name=WebhookRequestType]")
    .val(ddns ? ddns.WebhookRequestType : 1);
  if (ddns && ddns.EnableIPv4) {
    modal.find(".ui.enableipv4.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.enableipv4.checkbox").checkbox("set unchecked");
  }
  if (ddns && ddns.EnableIPv6) {
    modal.find(".ui.enableipv6.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.enableipv6.checkbox").checkbox("set unchecked");
  }
  showFormModal(
    ".ddns.modal",
    "#ddnsForm",
    "/api/ddns"
  );
}

function addOrEditNAT(nat) {
  const modal = $(".nat.modal");
  modal.children(".header").text((nat ? LANG.Edit : LANG.Add));
  modal
    .find(".server-primary-btn.button")
    .html(
      nat
        ? LANG.Edit + '<i class="edit icon"></i>'
        : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("input[name=ID]").val(nat ? nat.ID : null);
  modal.find("input[name=ServerID]").val(nat ? nat.ServerID : null);
  modal.find("input[name=Name]").val(nat ? nat.Name : null);
  modal.find("input[name=Host]").val(nat ? nat.Host : null);
  modal.find("input[name=Domain]").val(nat ? nat.Domain : null);
  showFormModal(
    ".nat.modal",
    "#natForm",
    "/api/nat"
  );
}

function connectToServer(id) {
  post('/terminal', { Host: window.location.host, Protocol: window.location.protocol, ID: id })
}

function post(path, params, method = 'post') {
  const form = document.createElement('form');
  form.method = method;
  form.action = path;
  form.target = "_blank";

  for (const key in params) {
    if (params.hasOwnProperty(key)) {
      const hiddenField = document.createElement('input');
      hiddenField.type = 'hidden';
      hiddenField.name = key;
      hiddenField.value = params[key];
      form.appendChild(hiddenField);
    }
  }
  document.body.appendChild(form);
  form.submit();
  document.body.removeChild(form);
}

function issueNewApiToken(apiToken) {
  const modal = $(".api.modal");
  modal.children(".header").text((apiToken ? LANG.Edit : LANG.Add) + ' ' + "API Token");
  modal
      .find(".server-primary-btn.button")
    .html(
      apiToken ? LANG.Edit + '<i class="edit icon"></i>' : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("textarea[name=Note]").val(apiToken ? apiToken.Note : null);
  showFormModal(".api.modal", "#apiForm", "/api/token");
}

function addOrEditServer(server, conf) {
  const modal = $(".server.modal");
  modal.children(".header").text((server ? LANG.Edit : LANG.Add) + ' ' + LANG.Server);
  modal
    .find(".server-primary-btn.button")
    .html(
      server ? LANG.Edit + '<i class="edit icon"></i>' : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("input[name=id]").val(server ? server.ID : null);
  modal.find("input[name=name]").val(server ? server.Name : null);
  modal.find("input[name=Tag]").val(server ? server.Tag : null);
  modal.find("a.ui.label.visible").each((i, el) => {
    el.remove();
  });
  var ddns;
  if (server) {
    ddns = server.DDNSProfilesRaw;
    let serverList;
    try {
      serverList = JSON.parse(ddns);
    } catch (error) {
      serverList = "[]";
    }
    const node = modal.find("i.dropdown.icon.ddnsProfiles");
    for (let i = 0; i < serverList.length; i++) {
      node.after(
        '<a class="ui label transition visible" data-value="' +
        serverList[i] +
        '" style="display: inline-block !important;">ID:' +
        serverList[i] +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  modal
    .find("input[name=DDNSProfilesRaw]")
    .val(server ? "[]," + ddns.substr(1, ddns.length - 2) : "[]");
  modal
    .find("input[name=DisplayIndex]")
    .val(server ? server.DisplayIndex : null);
  modal.find("textarea[name=Note]").val(server ? server.Note : null);
  modal.find("textarea[name=PublicNote]").val(server ? server.PublicNote : null);
  if (server) {
    modal.find(".secret.field").attr("style", "");
    modal.find(".command.field").attr("style", "");
    modal.find(".command.hostSecret").text(server.Secret);
    modal.find("input[name=secret]").val(server.Secret);
  } else {
    modal.find(".secret.field").attr("style", "display:none");
    modal.find(".command.field").attr("style", "display:none");
    modal.find("input[name=secret]").val("");
  }
  if (server && server.EnableDDNS) {
    modal.find(".ui.enableddns.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.enableddns.checkbox").checkbox("set unchecked");
  }
  if (server && server.HideForGuest) {
    modal.find(".ui.hideforguest.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.hideforguest.checkbox").checkbox("set unchecked");
  }

  showFormModal(".server.modal", "#serverForm", "/api/server");
}

function addOrEditMonitor(monitor) {
  const modal = $(".monitor.modal");
  modal.children(".header").text((monitor ? LANG.Edit : LANG.Add) + ' ' + LANG.Monitor);
  modal
    .find(".server-primary-btn.button")
    .html(
      monitor ? LANG.Edit + '<i class="edit icon"></i>' : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("input[name=ID]").val(monitor ? monitor.ID : null);
  modal.find("input[name=Name]").val(monitor ? monitor.Name : null);
  modal.find("input[name=Target]").val(monitor ? monitor.Target : null);
  modal.find("input[name=Duration]").val(monitor && monitor.Duration ? monitor.Duration : 30);
  modal.find("select[name=Type]").val(monitor ? monitor.Type : 1);
  modal.find("select[name=Cover]").val(monitor ? monitor.Cover : 0);
  modal.find("input[name=NotificationTag]").val(monitor ? monitor.NotificationTag : null);
  if (monitor && monitor.EnableShowInService) {
    modal.find(".ui.nb-show-in-service.checkbox").checkbox("set checked")
  } else {
    modal.find(".ui.nb-show-in-service.checkbox").checkbox("set unchecked")
  }
  if (monitor && monitor.Notify) {
    modal.find(".ui.nb-notify.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.nb-notify.checkbox").checkbox("set unchecked");
  }
  modal.find("input[name=MaxLatency]").val(monitor ? monitor.MaxLatency : null);
  modal.find("input[name=MinLatency]").val(monitor ? monitor.MinLatency : null);
  if (monitor && monitor.LatencyNotify) {
    modal.find(".ui.nb-lt-notify.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.nb-lt-notify.checkbox").checkbox("set unchecked");
  }
  modal.find("a.ui.label.visible").each((i, el) => {
    el.remove();
  });
  if (monitor && monitor.EnableTriggerTask) {
    modal.find(".ui.nb-EnableTriggerTask.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.nb-EnableTriggerTask.checkbox").checkbox("set unchecked");
  }
  var servers;
  var failTriggerTasks;
  var recoverTriggerTasks;
  if (monitor) {
    servers = monitor.SkipServersRaw;
    const serverList = JSON.parse(servers || "[]");
    const node = modal.find("i.dropdown.icon.specificServer");
    for (let i = 0; i < serverList.length; i++) {
      node.after(
        '<a class="ui label transition visible" data-value="' +
        serverList[i] +
        '" style="display: inline-block !important;">ID:' +
        serverList[i] +
        '<i class="delete icon"></i></a>'
      );
    }

    failTriggerTasks = monitor.FailTriggerTasksRaw;
    recoverTriggerTasks = monitor.RecoverTriggerTasksRaw;
    const failTriggerTasksList = JSON.parse(failTriggerTasks || "[]");
    const recoverTriggerTasksList = JSON.parse(recoverTriggerTasks || "[]");
    const node1 = modal.find("i.dropdown.icon.failTask");
    const node2 = modal.find("i.dropdown.icon.recoverTask");
    for (let i = 0; i < failTriggerTasksList.length; i++) {
      node1.after(
        '<a class="ui label transition visible" data-value="' +
        failTriggerTasksList[i] +
        '" style="display: inline-block !important;">ID:' +
        failTriggerTasksList[i] +
        '<i class="delete icon"></i></a>'
      );
    }
    for (let i = 0; i < recoverTriggerTasksList.length; i++) {
      node2.after(
        '<a class="ui label transition visible" data-value="' +
        recoverTriggerTasksList[i] +
        '" style="display: inline-block !important;">ID:' +
        recoverTriggerTasksList[i] +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  modal
    .find("input[name=FailTriggerTasksRaw]")
    .val(monitor ? "[]," + failTriggerTasks.substr(1, failTriggerTasks.length - 2) : "[]");
  modal
    .find("input[name=RecoverTriggerTasksRaw]")
    .val(monitor ? "[]," + recoverTriggerTasks.substr(1, recoverTriggerTasks.length - 2) : "[]");

  modal
    .find("input[name=SkipServersRaw]")
    .val(monitor ? "[]," + servers.substr(1, servers.length - 2) : "[]");
  showFormModal(".monitor.modal", "#monitorForm", "/api/monitor");
}
function addOrEditCron(cron) {
  const modal = $(".cron.modal");
  modal.children(".header").text((cron ? LANG.Edit : LANG.Add) + ' ' + LANG.Cron);
  modal
    .find(".server-primary-btn.button")
    .html(
      cron ? LANG.Edit + '<i class="edit icon"></i>' : LANG.Add + '<i class="add icon"></i>'
    );
  modal.find("input[name=ID]").val(cron ? cron.ID : null);
  modal.find("input[name=Name]").val(cron ? cron.Name : null);
  modal.find("select[name=TaskType]").val(cron ? cron.TaskType : 0);
  modal.find("select[name=Cover]").val(cron ? cron.Cover : 0);
  modal.find("input[name=NotificationTag]").val(cron ? cron.NotificationTag : null);
  modal.find("input[name=Scheduler]").val(cron ? cron.Scheduler : null);
  modal.find("a.ui.label.visible").each((i, el) => {
    el.remove();
  });
  var servers;
  if (cron) {
    servers = cron.ServersRaw;
    const serverList = JSON.parse(servers || "[]");
    const node = modal.find("i.dropdown.icon");
    for (let i = 0; i < serverList.length; i++) {
      node.after(
        '<a class="ui label transition visible" data-value="' +
        serverList[i] +
        '" style="display: inline-block !important;">ID:' +
        serverList[i] +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  modal
    .find("input[name=ServersRaw]")
    .val(cron ? "[]," + servers.substr(1, servers.length - 2) : "[]");
  modal.find("textarea[name=Command]").val(cron ? cron.Command : null);
  if (cron && cron.PushSuccessful) {
    modal.find(".ui.push-successful.checkbox").checkbox("set checked");
  } else {
    modal.find(".ui.push-successful.checkbox").checkbox("set unchecked");
  }
  showFormModal(".cron.modal", "#cronForm", "/api/cron");
}

function deleteRequest(api) {
  $.ajax({
    url: api,
    type: "DELETE",
  })
    .done((resp) => {
      if (resp.code == 200) {
        if (resp.message) {
          alert(resp.message);
        } else {
          alert("删除成功");
        }
        window.location.reload();
      } else {
        alert("删除失败 " + resp.code + "：" + resp.message);
        confirmBtn.toggleClass("loading");
      }
    })
    .fail((err) => {
      alert("网络错误：" + err.responseText);
    });
}

function manualTrigger(btn, cronId) {
  $(btn).toggleClass("loading");
  $.ajax({
    url: "/api/cron/" + cronId + "/manual",
    type: "GET",
  })
    .done((resp) => {
      $(btn).toggleClass("loading");
      if (resp.code == 200) {
        $.suiAlert({
          title: "触发成功，等待执行结果",
          type: "success",
          description: resp.message,
          time: "3",
          position: "top-center",
        });
      } else {
        $.suiAlert({
          title: "触发失败 ",
          type: "error",
          description: resp.code + "：" + resp.message,
          time: "3",
          position: "top-center",
        });
      }
    })
    .fail((err) => {
      $(btn).toggleClass("loading");
      $.suiAlert({
        title: "触发失败 ",
        type: "error",
        description: "网络错误：" + err.responseText,
        time: "3",
        position: "top-center",
      });
    });
}

function logout(id) {
  $.post("/api/logout", JSON.stringify({ id: id }))
    .done(function (resp) {
      if (resp.code == 200) {
        $.suiAlert({
          title: "注销成功",
          type: "success",
          description: "如需继续访问请使用 GitHub 再次登录",
          time: "3",
          position: "top-center",
        });
        window.location.reload();
      } else {
        $.suiAlert({
          title: "注销失败",
          description: resp.code + "：" + resp.message,
          type: "error",
          time: "3",
          position: "top-center",
        });
      }
    })
    .fail(function (err) {
      $.suiAlert({
        title: "网络错误",
        description: err.responseText,
        type: "error",
        time: "3",
        position: "top-center",
      });
    });
}

$(document).ready(() => {
  try {
    $(".ui.servers.search.dropdown").dropdown({
      clearable: true,
      apiSettings: {
        url: "/api/search-server?word={query}",
        cache: false,
      },
    });
  } catch (error) { }
});

$(document).ready(() => {
  try {
    $(".ui.tasks.search.dropdown").dropdown({
      clearable: true,
      apiSettings: {
        url: "/api/search-tasks?word={query}",
        cache: false,
      },
    });
  } catch (error) { }
});

$(document).ready(() => {
  try {
    $(".ui.ddns.search.dropdown").dropdown({
      clearable: true,
      apiSettings: {
        url: "/api/search-ddns?word={query}",
        cache: false,
      },
    });
  } catch (error) { }
});

// ===== 流量数据处理相关函数 =====

/**
 * 通过API获取特定服务器的流量数据
 * @param {string|number} serverId 服务器ID
 * @returns {Promise} API请求Promise
 */
function fetchTrafficData(serverId) {
  return $.ajax({
    url: `/api/server/${serverId}/traffic`,
    type: "GET",
    contentType: "application/json",
  }).done((resp) => {
    if (resp.code == 200) {
      return resp.data;
    } else {
      console.error("获取流量数据失败:", resp.message);
      return null;
    }
  }).fail((err) => {
    console.error("获取流量数据网络错误:", err.responseText);
    return null;
  });
}

/**
 * 调试并修复离线服务器的配置显示问题
 * 确保即使某些字段为空，前端也能正常显示离线服务器信息
 */
function debugOfflineServersConfig() {
  // 检查全局状态数据是否存在
  if (!window.statusCards || !window.statusCards.servers) {
    return;
  }
  
  // 遍历所有服务器，检查并修复离线服务器的配置数据
  window.statusCards.servers.forEach(server => {
    if (!server.IsOnline) {
      // 确保Host对象存在
      if (!server.Host) {
        server.Host = {};
      }
      
      // 确保CPU数组存在且不为null
      if (!server.Host.CPU || !Array.isArray(server.Host.CPU)) {
        server.Host.CPU = [];
      }
      
      // 确保GPU数组存在且不为null
      if (!server.Host.GPU || !Array.isArray(server.Host.GPU)) {
        server.Host.GPU = [];
      }
      
      // 确保其他必要的属性存在
      if (!server.Host.MemTotal) {
        server.Host.MemTotal = 0;
      }
      
      if (!server.Host.Platform) {
        server.Host.Platform = "";
      }
      
      if (!server.Host.PlatformVersion) {
        server.Host.PlatformVersion = "";
      }
      
      if (!server.Host.Virtualization) {
        server.Host.Virtualization = "";
      }
      
      if (!server.Host.Arch) {
        server.Host.Arch = "";
      }
    }
  });
  
  // 触发Vue更新
  if (typeof window.statusCards.updateServerData === 'function') {
    window.statusCards.updateServerData();
  }
}

// 添加流量数据API轮询
function updateTrafficData() {
    $.ajax({
        url: '/api/traffic',
        type: 'GET',
        success: function(data) {
            if (data && data.code === 200) {
                window.serverTrafficRawData = data.data;
                window.extractTrafficData();
            } else if (data && data.code === 403) {
                console.warn('需要登录才能访问流量数据');
                // 如果未登录,重定向到登录页面
                if (window.location.pathname !== '/login') {
                    window.location.href = '/login';
                }
            }
        },
        error: function(err) {
            console.error('获取流量数据失败:', err);
        }
    });
}

// 在页面加载完成后初始化
$(document).ready(function() {
    // 等待状态数据加载完成后再运行调试
    setTimeout(function() {
        debugOfflineServersConfig();
    }, 1000);
    
    // 每30秒检查一次，确保数据始终正确
    setInterval(debugOfflineServersConfig, 30000);
    
    // 原有的流量数据提取
    setTimeout(window.extractTrafficData, 500);
    setInterval(window.extractTrafficData, 30000);
    
    // 添加流量数据API轮询
    setTimeout(updateTrafficData, 1000);
    setInterval(updateTrafficData, 30000);
});

// 初始化全局流量数据变量
window.serverTrafficData = {};
window.lastTrafficUpdateTime = 0;

/**
 * 从嵌入的JavaScript变量中提取流量数据并进行处理
 */
window.extractTrafficData = function() {
    try {
        // Get traffic data from global variable
        const trafficItems = window.serverTrafficRawData || [];
        if (!trafficItems || trafficItems.length === 0) {
            console.warn("没有可用的流量数据");
            return;
        }
        
        const newTrafficData = {};
        let updatedCount = 0;
        
        // Process each traffic data item
        trafficItems.forEach(item => {
            if (item) {
                const rawServerId = item.id;
                const serverName = item.serverName;
                const maxTrafficText = item.max;
                const usedTrafficText = item.used;
                const percentStr = String(item.percent);
                
                // Find matching server
                let matchingServer = null;
                if (window.statusCards && window.statusCards.servers) {
                    matchingServer = window.statusCards.servers.find(s => s.Name === serverName);
                }
                
                const serverId = matchingServer ? matchingServer.ID : rawServerId;
                
                // Normalize unit format
                const standardMaxTraffic = window.formatTrafficUnit(maxTrafficText);
                const standardUsedTraffic = window.formatTrafficUnit(usedTrafficText);
                
                // Convert traffic data to bytes for accurate calculation
                const maxTrafficBytes = window.parseTrafficToBytes(maxTrafficText);
                const usedTrafficBytes = window.parseTrafficToBytes(usedTrafficText);
                
                // 修复：重新计算百分比，使用正确的算法计算使用比例
                let calculatedPercent = 0;
                if (maxTrafficBytes > 0) {
                    // 使用标准的百分比计算方法，而不是依赖服务器发送的百分比
                    calculatedPercent = Math.min(100, Math.max(0, Math.round((usedTrafficBytes / maxTrafficBytes) * 100)));
                }
                
                // Store data
                newTrafficData[serverId] = {
                    max: standardMaxTraffic,
                    used: standardUsedTraffic,
                    percent: calculatedPercent,
                    maxBytes: maxTrafficBytes,
                    usedBytes: usedTrafficBytes,
                    lastUpdate: Date.now()
                };
                
                // Store a copy with original ID as well
                if (serverId !== rawServerId) {
                    newTrafficData[rawServerId] = {
                        max: standardMaxTraffic,
                        used: standardUsedTraffic,
                        percent: calculatedPercent,
                        maxBytes: maxTrafficBytes,
                        usedBytes: usedTrafficBytes,
                        lastUpdate: Date.now()
                    };
                }
                
                updatedCount++;
            }
        });
        
        window.serverTrafficData = newTrafficData;
        window.lastTrafficUpdateTime = Date.now();
        
        // Update Vue component if available
        if (window.statusCards && typeof window.statusCards.updateTrafficData === 'function') {
            window.statusCards.updateTrafficData();
            console.log(`已更新 ${updatedCount} 个服务器的流量数据`);
        }
    } catch (e) {
        console.error('处理流量数据时出错:', e);
    }
};

/**
 * 将流量字符串（如"1.5MB"）解析为字节数
 * @param {string} trafficStr 流量字符串
 * @returns {number} 字节数
 */
window.parseTrafficToBytes = function(trafficStr) {
  if (!trafficStr) return 0;
  
  // 清理字符串，去除空格并转为大写，便于处理
  const cleanStr = trafficStr.replace(/\s+/g, '').toUpperCase();
  
  // 匹配标准格式 数字+单位
  let match = cleanStr.match(/^([\d.,]+)([KMGTPEZY]?B)$/i);
  
  // 如果没匹配到，尝试没有B的格式（如 1.5M）
  if (!match) {
    match = cleanStr.match(/^([\d.,]+)([KMGTPEZY])$/i);
    if (match) {
      match[2] = match[2] + 'B'; // 补充B单位
    }
  }
  
  // 如果还是没匹配到，返回0
  if (!match) {
    console.warn('无法解析流量字符串:', trafficStr);
    return 0;
  }
  
  // 处理千位分隔符
  const value = parseFloat(match[1].replace(/,/g, ''));
  const unit = match[2].toUpperCase();
  
  // 单位换算表
  const units = {
    'B': 1,
    'KB': 1024,
    'MB': 1024 * 1024,
    'GB': 1024 * 1024 * 1024,
    'TB': 1024 * 1024 * 1024 * 1024,
    'PB': 1024 * 1024 * 1024 * 1024 * 1024,
    'EB': 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
    'ZB': 1024 * 1024 * 1024 * 1024 * 1024 * 1024 * 1024,
    'YB': 1024 * 1024 * 1024 * 1024 * 1024 * 1024 * 1024 * 1024
  };
  
  return value * (units[unit] || 1);
};

/**
 * 格式化流量单位，确保统一格式（如总是包含B后缀）
 * @param {string} trafficStr 流量字符串
 * @returns {string} 格式化后的流量字符串
 */
window.formatTrafficUnit = function(trafficStr) {
  if (!trafficStr) return '0B';
  
  // 清理输入字符串，去除多余空格
  const cleanStr = trafficStr.trim();
  
  // 如果已经是标准格式（包含B），进行格式规范化
  const matchWithB = cleanStr.match(/^([\d.,]+)\s*([KMGTPEZY]?B)$/i);
  if (matchWithB) {
    const value = matchWithB[1];
    const unit = matchWithB[2].toUpperCase();
    // 确保单位统一大写格式
    return `${value}${unit}`;
  }
  
  // 处理省略了B的情况（如1.5M, 2.3G等）
  const matchWithoutB = cleanStr.match(/^([\d.,]+)\s*([KMGTPEZY])$/i);
  if (matchWithoutB) {
    const value = matchWithoutB[1];
    const unit = matchWithoutB[2].toUpperCase();
    return `${value}${unit}B`;
  }
  
  // 处理只有数字的情况，默认为字节
  const matchNumberOnly = cleanStr.match(/^([\d.,]+)$/);
  if (matchNumberOnly) {
    return `${matchNumberOnly[1]}B`;
  }
  
  // 解析为字节数并重新格式化
  const bytes = window.parseTrafficToBytes(trafficStr);
  if (bytes > 0) {
    const i = Math.floor(Math.log(bytes) / Math.log(1024));
    const sizes = ["B", "KB", "MB", "GB", "TB", "PB", "EB", "ZB", "YB"];
    const value = parseFloat((bytes / Math.pow(1024, i)).toFixed(2));
    return `${value}${sizes[i]}`;
  }
  
  return trafficStr; // 无法识别时返回原始字符串
};

// Traffic Data Management Utility Class
class TrafficManager {
    constructor() {
        this.trafficData = {};
        this.lastUpdateTime = 0;
        this.updateInterval = null;
        this.dataVersion = 0;
        this.callbacks = new Set();
    }

    // Parse traffic string to bytes
    static parseTrafficToBytes(trafficStr) {
        if (!trafficStr) return 0;
        const cleanStr = trafficStr.replace(/\s+/g, '').toUpperCase();
        let match = cleanStr.match(/^([\d.,]+)([KMGTPEZY]?B)$/i);
        if (!match) {
            match = cleanStr.match(/^([\d.,]+)([KMGTPEZY])$/i);
            if (match) match[2] = match[2] + 'B';
        }
        if (!match) return 0;
        
        const value = parseFloat(match[1].replace(/,/g, ''));
        const unit = match[2].toUpperCase();
        const units = {
            'B': 1,
            'KB': 1024,
            'MB': 1024 * 1024,
            'GB': 1024 * 1024 * 1024,
            'TB': 1024 * 1024 * 1024 * 1024,
            'PB': 1024 * 1024 * 1024 * 1024 * 1024
        };
        return value * (units[unit] || 1);
    }

    // Format bytes to human readable string
    static formatTrafficSize(bytes, decimals = 2) {
        if (!bytes || isNaN(bytes)) return '0B';
        const k = 1024;
        const sizes = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
        const i = Math.floor(Math.log(bytes) / Math.log(k));
        return `${parseFloat((bytes / Math.pow(k, i)).toFixed(decimals))}${sizes[i]}`;
    }

    // Standardize traffic unit display
    static standardizeTrafficUnit(trafficStr) {
        if (!trafficStr) return '0B';
        if (/^[\d.,]+\s*[KMGTPEZY]B$/i.test(trafficStr)) return trafficStr;
        const bytes = TrafficManager.parseTrafficToBytes(trafficStr);
        return TrafficManager.formatTrafficSize(bytes);
    }

    // Calculate traffic percentage
    static calculateTrafficPercent(used, max) {
        if (!used || !max) return 0;
        const usedBytes = typeof used === 'number' ? used : TrafficManager.parseTrafficToBytes(used);
        const maxBytes = typeof max === 'number' ? max : TrafficManager.parseTrafficToBytes(max);
        if (maxBytes === 0) return 0;
        return Math.min(100, Math.max(0, Math.round((usedBytes / maxBytes) * 100)));
    }

    // Process raw traffic data
    processTrafficData(rawData) {
        if (!Array.isArray(rawData) || rawData.length === 0) return;

        const newData = {};
        rawData.forEach(item => {
            if (!item || !item.server_id) return;

            const serverId = String(item.server_id);
            const maxTraffic = item.max || '0B';
            const usedTraffic = item.used || '0B';
            
            const standardMax = TrafficManager.standardizeTrafficUnit(maxTraffic);
            const standardUsed = TrafficManager.standardizeTrafficUnit(usedTraffic);
            const maxBytes = TrafficManager.parseTrafficToBytes(maxTraffic);
            const usedBytes = TrafficManager.parseTrafficToBytes(usedTraffic);
            const percent = TrafficManager.calculateTrafficPercent(usedBytes, maxBytes);

            newData[serverId] = {
                max: standardMax,
                used: standardUsed,
                maxBytes: maxBytes,
                usedBytes: usedBytes,
                percent: percent,
                serverName: item.server_name || 'Unknown',
                cycleName: item.cycle_name || 'Default',
                lastUpdate: Date.now()
            };
        });

        this.trafficData = newData;
        this.lastUpdateTime = Date.now();
        this.dataVersion++;
        this.notifySubscribers();
    }

    // Get traffic data for a specific server
    getServerTrafficData(serverId) {
        if (!serverId) return null;
        const serverIdStr = String(serverId).trim();
        
        // Try exact match
        if (this.trafficData[serverIdStr]) {
            return this.trafficData[serverIdStr];
        }

        // Try numeric match
        const numericId = parseInt(serverIdStr);
        if (!isNaN(numericId)) {
            const keys = Object.keys(this.trafficData);
            for (const key of keys) {
                if (parseInt(key) === numericId) {
                    return this.trafficData[key];
                }
            }
        }

        return null;
    }

    // Subscribe to data updates
    subscribe(callback) {
        if (typeof callback === 'function') {
            this.callbacks.add(callback);
        }
    }

    // Unsubscribe from data updates
    unsubscribe(callback) {
        this.callbacks.delete(callback);
    }

    // Notify all subscribers of data updates
    notifySubscribers() {
        this.callbacks.forEach(callback => {
            try {
                callback(this.trafficData, this.dataVersion);
            } catch (e) {
                console.error('Error in traffic data subscriber:', e);
            }
        });
    }

    // Start automatic updates
    startAutoUpdate(interval = 30000) {
        if (this.updateInterval) {
            clearInterval(this.updateInterval);
        }
        this.updateInterval = setInterval(() => {
            if (window.serverTrafficRawData) {
                this.processTrafficData(window.serverTrafficRawData);
            }
        }, interval);
    }

    // Stop automatic updates
    stopAutoUpdate() {
        if (this.updateInterval) {
            clearInterval(this.updateInterval);
            this.updateInterval = null;
        }
    }
}

// Create global instance
window.trafficManager = new TrafficManager();

// Initialize on page load
document.addEventListener('DOMContentLoaded', () => {
    if (window.serverTrafficRawData) {
        window.trafficManager.processTrafficData(window.serverTrafficRawData);
    }
    window.trafficManager.startAutoUpdate();
});

// WebSocket连接，用于实时更新服务器数据
(function() {
    const wsProtocol = window.location.protocol == "https:" ? "wss" : "ws";
    const wsUrl = wsProtocol + '://' + window.location.host + '/ws';
    
    function initWebSocket() {
        const ws = new WebSocket(wsUrl);
        
        ws.onopen = function() {
            console.log("WebSocket连接已建立");
        };
        
        ws.onmessage = function(evt) {
            try {
                const data = JSON.parse(evt.data);
                if (data && data.servers && Array.isArray(data.servers)) {
                    // 确保statusCards实例存在
                    if (window.statusCards) {
                        statusCards.servers = data.servers;
                        statusCards.updateServerLiveStatus(data);
                        
                        // 添加流量数据更新
                        if (data.trafficData) {
                            window.serverTrafficRawData = data.trafficData;
                            window.extractTrafficData();
                            console.log("收到流量数据更新");
                        }
                    }
                } else {
                    console.warn("WebSocket接收到无效的服务器数据格式");
                }
            } catch (e) {
                console.error("处理WebSocket消息时出错:", e);
            }
        };
        
        ws.onclose = function() {
            console.log("WebSocket连接已关闭，3秒后重试...");
            // 连接关闭后3秒重连
            setTimeout(initWebSocket, 3000);
        };
        
        ws.onerror = function(err) {
            console.error("WebSocket错误:", err);
        };
    }
    
    // 初始化WebSocket连接
    initWebSocket();
})();

/**
 * 获取单个服务器的流量数据
 * @param {string|number} serverId 服务器ID
 * @returns {Promise} 返回Promise对象
 */
function fetchServerTrafficData(serverId) {
    return $.ajax({
        url: `/api/server/${serverId}/traffic`,
        type: 'GET',
        success: function(data) {
            if (data && data.code === 200) {
                return data.data;
            } else if (data && data.code === 403) {
                console.warn('需要登录才能访问流量数据');
                if (window.location.pathname !== '/login') {
                    window.location.href = '/login';
                }
            }
            return null;
        },
        error: function(err) {
            console.error('获取服务器流量数据失败:', err);
            return null;
        }
    });
} 