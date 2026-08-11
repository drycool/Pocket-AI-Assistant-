// =============================================================
//  Portable Firefox — принудительная конфигурация «нулевого следа»
//  Файл перезаписывает prefs.js при каждом старте.
//  ВСЁ изменяемое (кеш, куки, сессии, загрузки) — на USB (Data/).
//  В гостовую ОС ничего не пишется.
// =============================================================

// ---- Кеш и данные строго на USB ----
user_pref("browser.cache.disk.enable", true);                 // кеш диска (внутри профиля на USB)
user_pref("browser.cache.disk.capacity", 512000);             // ~512 МБ
user_pref("browser.cache.memory.enable", true);
user_pref("browser.sessionhistory.max_entries", 50);
user_pref("browser.sessionstore.interval", 15000);            // автозакладка сессии каждые 15с
user_pref("browser.sessionstore.resume_from_crash", true);    // восстановление после аварии
user_pref("browser.sessionstore.max_resumed_crashes", 3);

// ---- Загрузки на USB ----
user_pref("browser.download.useDownloadDir", true);
user_pref("browser.download.dir", "X:\\Data\\downloads");     // переопределяется скриптом
user_pref("browser.download.folderList", 2);
user_pref("browser.download.manager.retention", 2);

// ---- Куки: сохраняем на USB для авторизации в ИИ-сервисах ----
user_pref("network.cookie.lifetimeBehavior", 0);              // 0 = обычные, сохраняются в профиле USB
user_pref("network.cookie.cookieBehavior", 0);

// ---- Отключаем телеметрию, обновления и отчётность ----
user_pref("app.update.auto", false);
user_pref("app.update.enabled", false);
user_pref("datareporting.policy.dataSubmissionEnabled", false);
user_pref("datareporting.healthreport.uploadEnabled", false);
user_pref("toolkit.telemetry.enabled", false);
user_pref("toolkit.telemetry.unified", false);
user_pref("browser.ping-centre.telemetry", false);
user_pref("beacon.enabled", false);

// ---- Безопасность / приватность ----
user_pref("browser.safebrowsing.enabled", false);
user_pref("browser.safebrowsing.downloads.enabled", false);
user_pref("signon.autofillForms", true);                      // автозаполнение форм авторизации
user_pref("signon.rememberSignons", true);                    // храним логины в профиле USB
user_pref("extensions.autoDisableScopes", 0);

// ---- Аппаратное ускорение и стабильность ----
user_pref("gfx.webrender.all", true);
user_pref("network.http.max-persistent-connections-per-server", 8);

// ---- Отключаем «сообщения о сбоях» в гостевую ОС ----
user_pref("breakpad.reportURL", "");
user_pref("browser.tabs.crashReporting.sendReport", false);

// ---- Скриншоты (собственные средства) ----
user_pref("extensions.screenshots.disabled", true);           // системные скриншоты через внешние тулзы
