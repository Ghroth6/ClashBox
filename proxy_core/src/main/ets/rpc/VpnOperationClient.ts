import { VpnOperationResult } from './VpnLifecycle';

// The RPC envelope contains a JSON result string. Never coerce it to boolean:
// both a failure object and the string "false" are truthy in JavaScript.
export function decodeVpnOperationResult(raw: string | number | boolean | undefined): VpnOperationResult {
  if (typeof raw !== 'string') throw new Error('VPN 服务返回了不兼容的操作结果');
  const result = JSON.parse(raw) as VpnOperationResult;
  if (result === null || typeof result !== 'object' ||
    !Number.isSafeInteger(result.generation) || result.generation < 0 ||
    !['Stopped', 'Starting', 'Running', 'Stopping', 'CleanupFailed'].includes(result.state) ||
    typeof result.stage !== 'string' || typeof result.error !== 'string' ||
    !Array.isArray(result.cleanupErrors) || result.cleanupErrors.some((error: string) => typeof error !== 'string')) {
    throw new Error('VPN 服务返回了无效的操作结果');
  }
  return result;
}

export function requireVpnOperationState(result: VpnOperationResult, expected: string): boolean {
  if (result.state !== expected || result.error !== '' || result.cleanupErrors.length > 0) {
    const details = [result.error, ...result.cleanupErrors].filter((value: string) => value !== '').join('; ');
    throw new Error(`VPN ${result.stage}: ${details || result.state}`);
  }
  return true;
}
