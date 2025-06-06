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
  const jsonString = JSON.stringify(data);
  return $.ajax({
    url: url,
    type: "POST",
    contentType: "application/json",
    data: jsonString,
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
                // 直接使用隐藏input的值，它已经是正确的JSON格式
                if (item.value && item.value.length > 2) {
                  try {
                    // 验证是否为有效的JSON数组
                    var parsedValue = JSON.parse(item.value);
                    if (Array.isArray(parsedValue)) {
                      obj[item.name] = item.value;
                    } else {
                      obj[item.name] = "[]";
                    }
                  } catch (e) {
                    // 如果不是有效JSON，尝试从字符串中提取数字
                    obj[item.name] = JSON.stringify(
                      [...item.value.matchAll(/\d+/gm)].map((k) =>
                        parseInt(k[0])
                      )
                    );
                  }
                } else {
                  obj[item.name] = "[]";
                }
              }

              if (item.name.endsWith("TasksRaw")) {
                // 直接使用隐藏input的值，它已经是正确的JSON格式
                if (item.value && item.value.length > 2) {
                  try {
                    // 验证是否为有效的JSON数组
                    var parsedValue = JSON.parse(item.value);
                    if (Array.isArray(parsedValue)) {
                      obj[item.name] = item.value;
                    } else {
                      obj[item.name] = "[]";
                    }
                  } catch (e) {
                    // 如果不是有效JSON，尝试从字符串中提取数字
                    obj[item.name] = JSON.stringify(
                      [...item.value.matchAll(/\d+/gm)].map((k) =>
                        parseInt(k[0])
                      )
                    );
                  }
                } else {
                  obj[item.name] = "[]";
                }
              }

              if (item.name.endsWith("DDNSProfilesRaw")) {
                // 直接使用隐藏input的值，它已经是正确的JSON格式
                if (item.value && item.value.length > 2) {
                  try {
                    // 验证是否为有效的JSON数组
                    var parsedValue = JSON.parse(item.value);
                    if (Array.isArray(parsedValue)) {
                      obj[item.name] = item.value;
                    } else {
                      obj[item.name] = "[]";
                    }
                  } catch (e) {
                    // 如果不是有效JSON，尝试从字符串中提取数字
                    obj[item.name] = JSON.stringify(
                      [...item.value.matchAll(/\d+/gm)].map((k) =>
                        parseInt(k[0])
                      )
                    );
                  }
                } else {
                  obj[item.name] = "[]";
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
        '" style="display: inline-block !important;">' +
        getTaskNameById(failTriggerTasksList[i]) +
        '<i class="delete icon"></i></a>'
      );
    }
    for (let i = 0; i < recoverTriggerTasksList.length; i++) {
      node2.after(
        '<a class="ui label transition visible" data-value="' +
        recoverTriggerTasksList[i] +
        '" style="display: inline-block !important;">' +
        getTaskNameById(recoverTriggerTasksList[i]) +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  modal
    .find("input[name=FailTriggerTasksRaw]")
    .val(rule ? failTriggerTasks : "[]");
  modal
    .find("input[name=RecoverTriggerTasksRaw]")
    .val(rule ? recoverTriggerTasks : "[]");

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
  var ddns = "[]";
  if (server) {
    ddns = server.DDNSProfilesRaw || "[]";
    let serverList;
    try {
      serverList = JSON.parse(ddns);
      // 确保它是一个数组
      if (!Array.isArray(serverList)) {
        console.error("DDNS配置不是数组格式:", ddns);
        serverList = [];
      }
    } catch (error) {
      console.error("解析DDNS配置出错:", error);
      serverList = [];
    }
    const node = modal.find("i.dropdown.icon.ddnsProfiles");
    for (let i = 0; i < serverList.length; i++) {
      node.after(
        '<a class="ui label transition visible" data-value="' +
        serverList[i] +
        '" style="display: inline-block !important;">' +
        getDDNSNameById(serverList[i]) +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  // 正确处理DDNS配置文件数组
  modal
    .find("input[name=DDNSProfilesRaw]")
    .val(server ? ddns : "[]");
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
        '" style="display: inline-block !important;">' +
        getServerNameById(serverList[i]) +
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
        '" style="display: inline-block !important;">' +
        getTaskNameById(failTriggerTasksList[i]) +
        '<i class="delete icon"></i></a>'
      );
    }
    for (let i = 0; i < recoverTriggerTasksList.length; i++) {
      node2.after(
        '<a class="ui label transition visible" data-value="' +
        recoverTriggerTasksList[i] +
        '" style="display: inline-block !important;">' +
        getTaskNameById(recoverTriggerTasksList[i]) +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  modal
    .find("input[name=FailTriggerTasksRaw]")
    .val(monitor ? failTriggerTasks : "[]");
  modal
    .find("input[name=RecoverTriggerTasksRaw]")
    .val(monitor ? recoverTriggerTasks : "[]");

  modal
    .find("input[name=SkipServersRaw]")
    .val(monitor ? servers : "[]");
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
        '" style="display: inline-block !important;">' +
        getServerNameById(serverList[i]) +
        '<i class="delete icon"></i></a>'
      );
    }
  }
  // 需要在 showFormModal 进一步拼接数组
  modal
    .find("input[name=ServersRaw]")
    .val(cron ? servers : "[]");
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
      // 当选择项目时更新隐藏input
      onChange: function(value, text, $choice) {
        var dropdown = $(this);
        var hiddenInput = dropdown.find('input[type="hidden"]');

        // 获取当前所有可见的标签
        var currentValues = [];
        dropdown.find('a.ui.label.visible').each(function() {
          var labelValue = $(this).attr('data-value');
          if (labelValue) {
            currentValues.push(parseInt(labelValue));
          }
        });

        hiddenInput.val(JSON.stringify(currentValues));
        console.log('服务器dropdown值更新:', currentValues);
      },
      // 添加删除标签的事件处理
      onLabelCreate: function(value, text) {
        var $label = this;
        var dropdown = $label.closest('.dropdown');

        $label.find('.delete.icon').on('click', function(e) {
          e.stopPropagation();
          $label.remove();

          // 更新隐藏的input值
          var hiddenInput = dropdown.find('input[type="hidden"]');
          var currentValues = [];
          dropdown.find('a.ui.label.visible').each(function() {
            var labelValue = $(this).attr('data-value');
            if (labelValue && $(this)[0] !== $label[0]) {
              currentValues.push(parseInt(labelValue));
            }
          });

          hiddenInput.val(JSON.stringify(currentValues));
          console.log('服务器删除后值更新:', currentValues);
        });
        return $label;
      }
    });
  } catch (error) {
    console.error('初始化服务器搜索dropdown失败:', error);
  }
});

$(document).ready(() => {
  try {
    $(".ui.tasks.search.dropdown").dropdown({
      clearable: true,
      apiSettings: {
        url: "/api/search-tasks?word={query}",
        cache: false,
      },
      // 当选择项目时更新隐藏input
      onChange: function(value, text, $choice) {
        var dropdown = $(this);
        var hiddenInput = dropdown.find('input[type="hidden"]');

        // 获取当前所有可见的标签
        var currentValues = [];
        dropdown.find('a.ui.label.visible').each(function() {
          var labelValue = $(this).attr('data-value');
          if (labelValue) {
            currentValues.push(parseInt(labelValue));
          }
        });

        hiddenInput.val(JSON.stringify(currentValues));
        console.log('任务dropdown值更新:', currentValues);
      },
      // 添加删除标签的事件处理
      onLabelCreate: function(value, text) {
        var $label = this;
        var dropdown = $label.closest('.dropdown');

        $label.find('.delete.icon').on('click', function(e) {
          e.stopPropagation();
          $label.remove();

          // 更新隐藏的input值
          var hiddenInput = dropdown.find('input[type="hidden"]');
          var currentValues = [];
          dropdown.find('a.ui.label.visible').each(function() {
            var labelValue = $(this).attr('data-value');
            if (labelValue && $(this)[0] !== $label[0]) {
              currentValues.push(parseInt(labelValue));
            }
          });

          hiddenInput.val(JSON.stringify(currentValues));
          console.log('任务删除后值更新:', currentValues);
        });
        return $label;
      }
    });
  } catch (error) {
    console.error('初始化任务搜索dropdown失败:', error);
  }
});

$(document).ready(() => {
  try {
    $(".ui.ddns.search.dropdown").dropdown({
      clearable: true,
      apiSettings: {
        url: "/api/search-ddns?word={query}",
        cache: false,
      },
      // 当选择项目时更新隐藏input
      onChange: function(value, text, $choice) {
        var dropdown = $(this);
        var hiddenInput = dropdown.find('input[type="hidden"]');

        // 获取当前所有可见的标签
        var currentValues = [];
        dropdown.find('a.ui.label.visible').each(function() {
          var labelValue = $(this).attr('data-value');
          if (labelValue) {
            currentValues.push(parseInt(labelValue));
          }
        });

        hiddenInput.val(JSON.stringify(currentValues));
        console.log('DDNS dropdown值更新:', currentValues);
      },
      // 添加删除标签的事件处理
      onLabelCreate: function(value, text) {
        var $label = this;
        var dropdown = $label.closest('.dropdown');

        $label.find('.delete.icon').on('click', function(e) {
          e.stopPropagation();
          $label.remove();

          // 更新隐藏的input值
          var hiddenInput = dropdown.find('input[type="hidden"]');
          var currentValues = [];
          dropdown.find('a.ui.label.visible').each(function() {
            var labelValue = $(this).attr('data-value');
            if (labelValue && $(this)[0] !== $label[0]) {
              currentValues.push(parseInt(labelValue));
            }
          });

          hiddenInput.val(JSON.stringify(currentValues));
          console.log('DDNS删除后值更新:', currentValues);
        });
        return $label;
      }
    });
  } catch (error) {
    console.error('初始化DDNS搜索dropdown失败:', error);
  }
});

// ===== 流量数据处理相关函数 =====

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

// 初始化WebSocket连接
function initWebSocket() {
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/ws`;
    
    const ws = new WebSocket(wsUrl);
    
    ws.onopen = function() {
        try {
            ws.send(JSON.stringify({ type: 'ping' }));
        } catch (e) {
        }
    };
    
    ws.onmessage = function(event) {
        try {
            const data = JSON.parse(event.data);
            
            // 处理服务器状态更新
            if (data.servers && Array.isArray(data.servers)) {
                if (window.statusCards) {
                    window.statusCards.updateServerLiveStatus(data);
                }
            }
            
            // 处理流量数据更新 - 流量数据可能单独发送或与服务器状态一起发送
            if (data.trafficData && Array.isArray(data.trafficData)) {
                window.serverTrafficRawData = data.trafficData;
                if (window.trafficManager) {
                    window.trafficManager.processTrafficData(data.trafficData);
                } else {
                    window.extractTrafficData();
                }
            }
        } catch (e) {
        }
    };
    
    ws.onerror = function(error) {
    };
    
    ws.onclose = function(event) {
        setTimeout(initWebSocket, 5000);
    };
    
    return ws;
}

// 在页面加载完成后初始化WebSocket
$(document).ready(function() {
    window.ws = initWebSocket();
    
    // 添加页面可见性变化监听
    document.addEventListener('visibilitychange', function() {
        if (document.visibilityState === 'visible') {
            if (!window.ws || window.ws.readyState !== WebSocket.OPEN) {
                window.ws = initWebSocket();
            }
        }
    });
});

// 保留WebSocket的流量数据处理
window.extractTrafficData = function() {
    try {
        const trafficItems = window.serverTrafficRawData || [];
        if (!trafficItems || trafficItems.length === 0) {
            return;
        }
        
        const newTrafficData = {};
        let updatedCount = 0;
        
        trafficItems.forEach(item => {
            if (item && item.server_id) {
                const serverId = String(item.server_id);
                const serverName = item.server_name || "Unknown Server";
                
                // 优先使用字节数据作为源数据
                let maxBytes, usedBytes, percent;
                
                if (item.is_bytes_source && typeof item.used_bytes === 'number' && typeof item.max_bytes === 'number') {
                    // 后端提供了字节数据源，直接使用
                    maxBytes = item.max_bytes;
                    usedBytes = item.used_bytes;
                    
                    // 从字节数据计算百分比
                    if (maxBytes > 0) {
                        percent = (usedBytes / maxBytes) * 100;
                        percent = Math.max(0, Math.min(100, percent)); // 限制在0-100范围
                    } else {
                        percent = 0;
                    }
                } else {
                    // 回退到解析格式化字符串
                    const maxTraffic = item.max_formatted || "0B";
                    const usedTraffic = item.used_formatted || "0B";
                    
                    maxBytes = window.parseTrafficToBytes ? window.parseTrafficToBytes(maxTraffic) : 0;
                    usedBytes = window.parseTrafficToBytes ? window.parseTrafficToBytes(usedTraffic) : 0;
                    
                    // 如果有后端计算的百分比，作为备选
                    if (typeof item.used_percent === 'number') {
                        percent = item.used_percent;
                    } else if (maxBytes > 0) {
                        percent = (usedBytes / maxBytes) * 100;
                        percent = Math.max(0, Math.min(100, percent));
                    } else {
                        percent = 0;
                    }
                }
                
                // 格式化显示字符串
                const standardMax = window.formatTrafficUnit ? window.formatTrafficUnit(readableBytes(maxBytes)) : readableBytes(maxBytes);
                const standardUsed = window.formatTrafficUnit ? window.formatTrafficUnit(readableBytes(usedBytes)) : readableBytes(usedBytes);
                
                newTrafficData[serverId] = {
                    max: standardMax,
                    used: standardUsed,
                    percent: Math.round(percent * 100) / 100,
                    maxBytes: maxBytes,
                    usedBytes: usedBytes,
                    serverName: serverName,
                    cycleName: item.cycle_name || "Unknown",
                    cycleStart: item.cycle_start,
                    cycleEnd: item.cycle_end,
                    cycleUnit: item.cycle_unit,
                    cycleInterval: item.cycle_interval,
                    lastUpdate: Date.now(),
                    isBytesSource: item.is_bytes_source || false
                };
                
                updatedCount++;
            }
        });
        
        window.serverTrafficData = newTrafficData;
        window.lastTrafficUpdateTime = Date.now();
        
        if (window.statusCards && typeof window.statusCards.updateTrafficData === 'function') {
            window.statusCards.updateTrafficData();
        }
    } catch (e) {
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

    // Process raw traffic data
    processTrafficData(rawData) {
        if (!Array.isArray(rawData) || rawData.length === 0) {
            return;
        }

        const newData = {};
        let updatedCount = 0;

        rawData.forEach(item => {
            if (!item || !item.server_id) return;

            const serverId = String(item.server_id);
            
            // 优先使用字节数据作为源数据
            let maxBytes, usedBytes, percent;
            
            if (item.is_bytes_source && typeof item.used_bytes === 'number' && typeof item.max_bytes === 'number') {
                // 后端提供了字节数据源，直接使用
                maxBytes = item.max_bytes;
                usedBytes = item.used_bytes;
                
                // 从字节数据计算百分比
                if (maxBytes > 0) {
                    percent = (usedBytes / maxBytes) * 100;
                    percent = Math.max(0, Math.min(100, percent)); // 限制在0-100范围
                } else {
                    percent = 0;
                }
            } else {
                // 回退到解析格式化字符串
                const maxTraffic = item.max_formatted || '0B';
                const usedTraffic = item.used_formatted || '0B';
                
                maxBytes = TrafficManager.parseTrafficToBytes(maxTraffic);
                usedBytes = TrafficManager.parseTrafficToBytes(usedTraffic);
                
                // 如果有后端计算的百分比，作为备选
                if (typeof item.used_percent === 'number') {
                    percent = item.used_percent;
                } else if (maxBytes > 0) {
                    percent = (usedBytes / maxBytes) * 100;
                    percent = Math.max(0, Math.min(100, percent));
                } else {
                    percent = 0;
                }
            }
            
            // 格式化显示字符串
            const standardMax = TrafficManager.formatTrafficSize(maxBytes);
            const standardUsed = TrafficManager.formatTrafficSize(usedBytes);

            newData[serverId] = {
                max: standardMax,
                used: standardUsed,
                percent: Math.round(percent * 100) / 100,
                maxBytes: maxBytes,
                usedBytes: usedBytes,
                serverName: item.server_name || 'Unknown',
                cycleName: item.cycle_name || 'Default',
                cycleStart: item.cycle_start,
                cycleEnd: item.cycle_end,
                cycleUnit: item.cycle_unit,
                cycleInterval: item.cycle_interval,
                lastUpdate: Date.now(),
                isBytesSource: item.is_bytes_source || false  // 记录数据源类型
            };
            
            updatedCount++;
        });

        this.trafficData = newData;
        this.lastUpdateTime = Date.now();
        this.dataVersion++;
        
        // 同时更新全局数据
        window.serverTrafficData = newData;
        window.lastTrafficUpdateTime = this.lastUpdateTime;
        
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
            // 立即发送当前数据
            callback(this.trafficData, this.dataVersion);
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
            }
        });
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
}

// Create global instance
window.trafficManager = new TrafficManager();

// Initialize on page load
document.addEventListener('DOMContentLoaded', () => {
    if (window.serverTrafficRawData) {
        window.trafficManager.processTrafficData(window.serverTrafficRawData);
    }
});

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
                if (window.location.pathname !== '/login') {
                    window.location.href = '/login';
                }
            }
            return null;
        },
        error: function(err) {
            return null;
        }
    });
}

// 修改Vue实例中的流量相关方法
if (window.statusCards) {
    // 获取流量显示
    window.statusCards.getTrafficDisplay = function(serverId) {
        const trafficData = this.getServerTrafficData(serverId);
        if (!trafficData) {
            return '0%';
        }
        
        // 确保我们使用有效的百分比值
        if (typeof trafficData.percent === 'number') {
            return trafficData.percent.toFixed(2) + '%';
        }
        
        // 如果没有直接提供百分比，尝试计算
        if (trafficData.maxBytes && trafficData.maxBytes > 0) {
            const percent = (trafficData.usedBytes / trafficData.maxBytes) * 100;
            return percent.toFixed(2) + '%';
        }
        
        return '0%';
    };

    // 获取流量提示
    window.statusCards.getTrafficTooltip = function(serverId) {
        const trafficData = this.getServerTrafficData(serverId);
        if (!trafficData || !trafficData.usedBytes || !trafficData.maxBytes) {
            return '无数据';
        }
        return `${this.formatByteSize(trafficData.usedBytes)} / ${this.formatByteSize(trafficData.maxBytes)}`;
    };

    // 获取服务器流量数据
    window.statusCards.getServerTrafficData = function(serverId) {
        if (!serverId || !window.serverTrafficData) {
            return null;
        }
        
        const serverIdStr = String(serverId).trim();
        
        // 尝试精确匹配
        if (window.serverTrafficData[serverIdStr]) {
            return window.serverTrafficData[serverIdStr];
        }
        
        // 尝试数字匹配
        const numericId = parseInt(serverIdStr);
        if (!isNaN(numericId)) {
            const keys = Object.keys(window.serverTrafficData);
            for (const key of keys) {
                if (parseInt(key) === numericId) {
                    return window.serverTrafficData[key];
                }
            }
        }
        
        return null;
    };
}

// 全局服务器ID到名称的映射
window.serverIdToName = {};

// 获取服务器名称的函数
function getServerNameById(serverId) {
  return window.serverIdToName[serverId] || `ID:${serverId}`;
}

// 更新服务器名称映射的函数
function updateServerNameMapping() {
  // 在页面加载时和每次修改后更新映射
  $.get('/api/search-server?word=')
    .done(function(resp) {
      if (resp.success && resp.results) {
        window.serverIdToName = {};
        resp.results.forEach(server => {
          window.serverIdToName[server.value] = server.name;
        });
      }
    })
    .fail(function() {
    });
}

// 全局DDNS ID到名称的映射
window.ddnsIdToName = {};

// 获取DDNS配置名称的函数
function getDDNSNameById(ddnsId) {
  return window.ddnsIdToName[ddnsId] || `ID:${ddnsId}`;
}

// 更新DDNS名称映射的函数
function updateDDNSNameMapping() {
  $.get('/api/search-ddns?word=')
    .done(function(resp) {
      if (resp.success && resp.results) {
        window.ddnsIdToName = {};
        resp.results.forEach(ddns => {
          window.ddnsIdToName[ddns.value] = ddns.name;
        });
      }
    })
    .fail(function() {
    });
}

// 全局Task ID到名称的映射
window.taskIdToName = {};

// 获取Task名称的函数
function getTaskNameById(taskId) {
  return window.taskIdToName[taskId] || `ID:${taskId}`;
}

// 更新Task名称映射的函数
function updateTaskNameMapping() {
  $.get('/api/search-tasks?word=')
    .done(function(resp) {
      if (resp.success && resp.results) {
        window.taskIdToName = {};
        resp.results.forEach(task => {
          window.taskIdToName[task.value] = task.name;
        });
      }
    })
    .fail(function() {
    });
}

// 在页面加载时初始化所有映射
$(document).ready(() => {
  updateServerNameMapping();
  updateDDNSNameMapping();
  updateTaskNameMapping();
});

// 统一的系统信息格式化
const SystemInfoFormatter = {
    os: {
        windows: i => (i || "").replace("Microsoft ", "").replace("Datacenter", "").replace("Service Pack 1", ""),
        default: i => {
            if (!i) return "";
            const osMapping = {
                "ubuntu": "Ubuntu",
                "debian": "Debian",
                "centos": "CentOS",
                "darwin": "MacOS",
                "redhat": "RedHat",
                "archlinux": "Archlinux",
                "coreos": "Coreos",
                "deepin": "Deepin",
                "fedora": "Fedora",
                "alpine": "Alpine",
                "tux": "Tux",
                "linuxmint": "LinuxMint",
                "oracle": "Oracle",
                "slackware": "SlackWare",
                "raspbian": "Raspbian",
                "gentoo": "GenToo",
                "arch": "Arch",
                "amazon": "Amazon",
                "xenserver": "XenServer",
                "scientific": "ScientificSL",
                "rhel": "Rhel",
                "rawhide": "RawHide",
                "cloudlinux": "CloudLinux",
                "ibm_powerkvm": "IBM",
                "almalinux": "Almalinux",
                "suse": "Suse",
                "opensuse": "OpenSuse",
                "opensuse-leap": "OpenSuse",
                "opensuse-tumbleweed": "OpenSuse",
                "opensuse-tumbleweed-kubic": "OpenSuse",
                "sles": "Sles",
                "sled": "Sled",
                "caasp": "Caasp",
                "exherbo": "ExherBo",
                "solus": "Solus"
            };
            return osMapping[(i || "").toLowerCase()] || i;
        }
    },
    
    virtualization: {
        mapping: {
            "kvm": "KVM",
            "openvz": "OpenVZ",
            "lxc": "LXC",
            "xen": "Xen",
            "vbox": "VirtualBox",
            "virtualbox": "VirtualBox",
            "rkt": "RKT",
            "docker": "Docker",
            "vmware": "VMware",
            "vmware-esxi": "VMware ESXi",
            "linux-vserver": "VServer",
            "hyperv": "Hyper-V",
            "hyper-v": "Hyper-V",
            "microsoft": "Hyper-V",
            "qemu": "QEMU",
            "parallels": "Parallels",
            "bhyve": "bhyve",
            "jail": "FreeBSD Jail",
            "zone": "Solaris Zone",
            "wsl": "WSL",
            "podman": "Podman",
            "containerd": "containerd",
            "systemd-nspawn": "systemd-nspawn"
        },
        format: function(i) {
            if (!i) return "";
            const normalized = (i || "").toString().toLowerCase();
            return this.mapping[normalized] || (normalized.charAt(0).toUpperCase() + normalized.slice(1));
        }
    }
};

// 简化的系统信息格式化函数
function specialOS(i) {
    if (!i) return "";
    const normalized = i.toString().toLowerCase();
    if (normalized.includes('windows')) {
        return SystemInfoFormatter.os.windows(i);
    }
    return SystemInfoFormatter.os.default(i);
}

function specialVir(i) {
    if (!i) return "";
    return SystemInfoFormatter.virtualization.format(i);
}

// 简化的字符串清理函数
function clearString(i) {
    if (!i) return Array.isArray(i) ? i : [i];
    const clean = s => (s || "").toString().replace(/(\r|\n|\"|\]|\[|\\)/ig, "");
    return Array.isArray(i) ? i.map(clean) : [clean(i)];
}

// 使用更现代的日期格式化
const DateFormatter = {
    format: function(date, format) {
        const pad = (n) => n.toString().padStart(2, '0');
        const replacements = {
            'yyyy': date.getFullYear(),
            'MM': pad(date.getMonth() + 1),
            'dd': pad(date.getDate()),
            'HH': pad(date.getHours()),
            'mm': pad(date.getMinutes()),
            'ss': pad(date.getSeconds()),
            'S': date.getMilliseconds()
        };
        
        return format.replace(/yyyy|MM|dd|HH|mm|ss|S/g, match => replacements[match]);
    }
};

// 更新Date原型方法
Date.prototype.format = function(format) {
    return DateFormatter.format(this, format);
};

$.suiAlert=function(i){function t(){l=setTimeout(function(){c.transition({animation:e,duration:"2s",onComplete:function(){c.remove()}})},1e3*o.time)}var o=$.extend({title:"Semantic UI Alerts",description:"semantic ui alerts library",type:"error",time:5,position:"top-right",icon:!1},i);o.icon===!1&&("info"==o.type?o.icon="announcement":"success"==o.type?o.icon="checkmark":"error"==o.type?o.icon="remove":"warning"==o.type&&(o.icon="warning circle"));var e="drop";"top-right"==o.position?e="fly left":"top-center"==o.position?e="fly down":"top-left"==o.position?e="fly right":"bottom-right"==o.position?e="fly left":"bottom-center"==o.position?e="fly up":"bottom-left"==o.position&&(e="fly right");var n="",r=$(window).width();r<425&&(n="mini");var s="ui-alerts."+o.position;$("body > ."+s).length||$("body").append('<div class="ui-alerts '+o.position+'"></div>');var c=$('<div class="ui icon floating '+n+" message "+o.type+'" id="alert"> <i class="'+o.icon+' icon"></i> <i class="close icon" id="alertclose"></i> <div class="content"> <div class="header">'+o.title+"</div> <p>"+o.description+"</p> </div> </div>");$("."+s).prepend(c),c.transition("pulse"),$("#alertclose").on("click",function(){$(this).closest("#alert").transition({animation:e,onComplete:function(){c.remove()}})});var l=0;$(c).mouseenter(function(){clearTimeout(l)}).mouseleave(function(){t()}),t()};
