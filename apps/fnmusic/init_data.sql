-- init_data.sql 只做数据播种（免登录 admin/oauth 等），不建任何表。
-- schema 必须由 trim-music 首次启动时用包内 sqlite.sql/sqlite_v2.sql/sqlite_v3.sql
-- 自行初始化（它还会同步创建 meta/lyric-sqlite/lyric-xx.db 分片库）。
-- 因此 fnmusic 会在 trim-music 建好 schema 之后再执行本脚本。
BEGIN TRANSACTION;
INSERT OR IGNORE INTO app_state VALUES(1,'initialized','true',NULL,'2026-09-19 23:33:45.756558128+08:00','2026-09-19 23:33:45.756558128+08:00');
INSERT OR IGNORE INTO app_state VALUES(2,'server_guid','85eaaedb968547e1a5e3b682eea3b2e0',NULL,'2026-09-19 23:33:45.756758039+08:00','2026-09-19 23:33:45.756758039+08:00');
INSERT OR IGNORE INTO app_state VALUES(3,'server_name','fnas',NULL,'2026-09-19 23:33:45.756890682+08:00','2026-09-19 23:33:45.756890682+08:00');
INSERT OR IGNORE INTO app_state VALUES(4,'server_lang','zh-CN',NULL,'2026-09-19 23:33:45.757044484+08:00','2026-09-19 23:33:45.757044484+08:00');
INSERT OR IGNORE INTO user VALUES(1,'c75e5199ddba491396b15033034a3f6d','admin','admin','$2a$12$eCqJX7ZPiQ96Nv3BO9asCOl7KaMAm2kJPjDhkR1R3sOr3aI9GBU9K','admin','oauth','active','2026-09-19 23:33:45.755769473+08:00','2026-09-19 23:41:45.546084412+08:00','all','2026-09-19 23:41:36.863001157+08:00');
INSERT OR IGNORE INTO user_oauth VALUES(1,1,0,'trim-nas','1000','everybody','2026-09-19 23:33:45.756282523+08:00','2026-09-19 23:33:45.756282523+08:00');
INSERT OR IGNORE INTO user_token VALUES(1,1,'13722b9ede0f45e082b501e72b2299de','a85202caf62c4660a10163030ee52ec5','2026-10-19 23:33:45.757496841+08:00',1,'2026-09-19 23:33:45.757524552+08:00','2026-09-19 23:33:45.757524552+08:00');
INSERT OR IGNORE INTO app_hook VALUES(1,'app_version','v3','2026-09-19 23:30:35.511769203+08:00','2026-09-19 23:30:35.530574498+08:00');
INSERT OR IGNORE INTO sqlite_sequence VALUES('app_hook',1);
INSERT OR IGNORE INTO sqlite_sequence VALUES('user',2);
INSERT OR IGNORE INTO sqlite_sequence VALUES('user_oauth',1);
INSERT OR IGNORE INTO sqlite_sequence VALUES('app_state',4);
INSERT OR IGNORE INTO sqlite_sequence VALUES('user_token',1);
COMMIT;
