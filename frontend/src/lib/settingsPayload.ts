const RESPONSE_CACHE_CONFIG_GENERATION = "response_cache_config_generation";
const CODEX_IMAGES_DEFAULT_MAIN_MODEL = "codex_images_default_main_model";
// 后端按当前 Resin 配置现算的只读摘要,回写没有意义。
const CODEX_EGRESS = "codex_egress";
// 进程启动时的 TLS 模式，只展示，回写会被当成未知设置。
const CODEX_TRANSPORT_MODE = "codex_transport_mode";

export function buildWritableSettingsPayload<
  T extends object,
>(settings: T): Omit<T, typeof RESPONSE_CACHE_CONFIG_GENERATION | typeof CODEX_IMAGES_DEFAULT_MAIN_MODEL | typeof CODEX_EGRESS | typeof CODEX_TRANSPORT_MODE> {
  const payload = { ...settings };
  Reflect.deleteProperty(payload, RESPONSE_CACHE_CONFIG_GENERATION);
  Reflect.deleteProperty(payload, CODEX_IMAGES_DEFAULT_MAIN_MODEL);
  Reflect.deleteProperty(payload, CODEX_EGRESS);
  Reflect.deleteProperty(payload, CODEX_TRANSPORT_MODE);
  return payload;
}
