import { JSON, util } from '@kit.ArkTS';

export interface ProfileResource {
  path: string;
  base64: string;
}

export interface ProfileBundle {
  format: string;
  configBase64: string;
  resources: ProfileResource[];
}

export interface ImportedFile {
  path: string;
  bytes: Uint8Array;
}

// Local, versioned transport only. YAML is never parsed/reserialized here.
// In particular, URL/provider downloads and scripts are not executed on import.
export function decodeProfileImport(input: Uint8Array): ImportedFile[] {
  if (input.length === 0 || input.length > 32 * 1024 * 1024) {
    throw new Error('配置文件为空或超过 32 MiB');
  }
  const text = util.TextDecoder.create('utf-8', { fatal: true }).decodeToString(input);
  if (!text.trimStart().startsWith('{')) {
    return [{ path: 'config.yaml', bytes: input }];
  }
  const bundle = JSON.parse(text) as ProfileBundle;
  if (bundle.format !== 'clashbox-profile-bundle-v1') {
    // JSON is also valid YAML; ordinary Mihomo JSON stays byte-for-byte intact.
    return [{ path: 'config.yaml', bytes: input }];
  }
  if (typeof bundle.configBase64 !== 'string' || !Array.isArray(bundle.resources) || bundle.resources.length > 256) {
    throw new Error('配置资源包结构无效');
  }
  const decoder = new util.Base64Helper();
  const config = decoder.decodeSync(bundle.configBase64);
  if (config.length === 0) throw new Error('配置资源包缺少配置');
  if (config.length > 24 * 1024 * 1024) throw new Error('配置资源总大小超过 24 MiB');
  util.TextDecoder.create('utf-8', { fatal: true }).decodeToString(config);
  const files: ImportedFile[] = [{ path: 'config.yaml', bytes: config }];
  const paths: string[] = ['config.yaml'];
  let total = config.length;
  bundle.resources.forEach((resource: ProfileResource) => {
    const path = resource.path;
    // No absolute paths, dot segments, backslashes, URI schemes or reserved config.
    if (typeof path !== 'string' || path.length > 240 ||
      !/^[A-Za-z0-9_\-./]+$/.test(path) || path.startsWith('/') ||
      path.split('/').some((part: string) => part === '' || part === '.' || part === '..') ||
      paths.some((existing: string) => existing === path.toLowerCase() ||
        existing.startsWith(path.toLowerCase() + '/') || path.toLowerCase().startsWith(existing + '/')) ||
      typeof resource.base64 !== 'string') {
      throw new Error('配置资源包路径无效或重复');
    }
    paths.push(path.toLowerCase());
    const bytes = decoder.decodeSync(resource.base64);
    total += bytes.length;
    if (total > 24 * 1024 * 1024) throw new Error('配置资源总大小超过 24 MiB');
    files.push({ path: path, bytes: bytes });
  });
  return files;
}
