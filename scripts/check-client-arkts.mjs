import { existsSync } from 'node:fs';
import { createRequire } from 'node:module';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

// Run with the declared IDE Node. This checks these standalone client modules,
// using the installed SDK's ArkTS checker and declarations; it is not a HAP build.
const repo = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const args = process.argv.slice(2);
if (args.length !== 0 && (args.length !== 2 || args[0] !== '--sdk-ets-dir')) {
  throw new Error('Usage: node scripts/check-client-arkts.mjs [--sdk-ets-dir <SDK openharmony/ets>]');
}
const sdk = path.resolve(args[1] ?? 'C:/Program Files/Huawei/DevEco Studio/sdk/default/openharmony/ets');
const loader = path.join(sdk, 'build-tools/ets-loader');
const compiler = path.join(loader, 'node_modules/typescript/lib/typescript.js');
if (!existsSync(compiler)) throw new Error(`SDK ArkTS checker not found: ${compiler}`);
const ts = createRequire(import.meta.url)(compiler);
if (typeof ts.ArkTSLinter_1_1?.runArkTSLinter !== 'function') throw new Error('SDK does not expose the ArkTS 1.1 checker');

const files = [
  'proxy_core/src/main/ets/rpc/NetworkSnapshot.ets',
  'proxy_core/src/main/ets/ProfileImport.ets',
].map(file => path.join(repo, file));
const options = {
  target: ts.ScriptTarget.ES2021,
  module: ts.ModuleKind.ES2020,
  moduleResolution: ts.ModuleResolutionKind.NodeJs,
  strict: true,
  noEmit: true,
  skipLibCheck: true,
  lib: ['lib.es2021.d.ts', 'lib.dom.d.ts'],
  baseUrl: repo,
  paths: {
    '@kit.*': [path.join(sdk, 'kits/@kit.*.d.ts')],
    '@ohos.*': [path.join(sdk, 'api/@ohos.*.d.ts')],
    '@system.*': [path.join(sdk, 'api/@system.*.d.ts')],
    'libflclash.so': [path.join(repo, 'proxy_core/src/main/cpp/types/libflclash/Index.d.ts')],
  },
  etsLoaderPath: loader,
  needDoArkTsLinter: true,
};
const builder = ts.createIncrementalProgram({ rootNames: files, options, host: ts.createIncrementalCompilerHost(options) });
const program = builder.getProgram();
const diagnostics = [];
function collect(phase, values) {
  diagnostics.push(...values.map(diagnostic => ({ phase, diagnostic })));
}
collect('compiler-options', program.getOptionsDiagnostics());
for (const file of files) {
  const source = program.getSourceFile(file);
  if (!source) throw new Error(`SDK checker did not load target: ${file}`);
  collect('compiler-syntax', program.getSyntacticDiagnostics(source));
  collect('compiler-semantics', program.getSemanticDiagnostics(source));
  // Same ArkTS 1.1 checker used by ets-loader; limit the review to each target
  // instead of importing unrelated application/third-party diagnostics.
  collect('arkts-linter', ts.ArkTSLinter_1_1.runArkTSLinter(builder, source, undefined, 'ArkTS_1_1'));
}
const unique = [...new Map(diagnostics.map(entry => [
  `${entry.phase}:${entry.diagnostic.file?.fileName}:${entry.diagnostic.start}:${entry.diagnostic.code}:${ts.flattenDiagnosticMessageText(entry.diagnostic.messageText, '\n')}`,
  entry,
])).values()];
let errors = 0, warnings = 0;
for (const { phase, diagnostic } of unique) {
  if (diagnostic.category === ts.DiagnosticCategory.Error) errors++;
  if (diagnostic.category === ts.DiagnosticCategory.Warning) warnings++;
  const position = diagnostic.file?.getLineAndCharacterOfPosition(diagnostic.start ?? 0);
  console.log(JSON.stringify({
    phase,
    file: diagnostic.file?.fileName,
    line: position === undefined ? undefined : position.line + 1,
    category: ts.DiagnosticCategory[diagnostic.category],
    code: diagnostic.code,
    message: ts.flattenDiagnosticMessageText(diagnostic.messageText, '\n'),
  }));
}
console.log(JSON.stringify({ scope: 'standalone client modules, not HAP', compiler, files, diagnostics: unique.length, errors, warnings }));
process.exitCode = errors > 0 ? 1 : 0;
