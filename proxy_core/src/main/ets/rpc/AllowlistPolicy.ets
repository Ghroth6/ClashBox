// This experimental build deliberately supports only an explicit app allowlist.
// Losing settings, switching to a denylist or clearing the list cannot broaden it.
export function requireAllowlist(enabled: boolean, mode: string, packages: string[]): string[] {
  if (!enabled || mode !== 'AcceptSelected') {
    throw new Error('本实验版仅支持开启应用白名单；请先选择出境易等目标应用');
  }
  if (!Array.isArray(packages) || packages.length === 0) {
    throw new Error('应用白名单为空，已拒绝启动 VPN');
  }
  const result: string[] = [];
  packages.forEach((name: string) => {
    if (typeof name !== 'string' || name.length > 128 ||
      !/^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z0-9_]+)+$/.test(name)) {
      throw new Error('白名单含无效原生包名，已拒绝启动 VPN');
    }
    if (!result.includes(name)) result.push(name);
  });
  return result;
}
