// Generate candidate registrations only. Never modifies the current OS defaults.
import { readFile, writeFile } from 'node:fs/promises';
const root = new URL('../../', import.meta.url);
const source = await readFile(new URL('plugins/api/externalopen/catalog.go', root), 'utf8');
const types = [...source.matchAll(/\{"([a-z0-9]+)", "(scheme|file)", "([^"]+)", "([a-z0-9]+)"\}/g)].map(m => ({ id: m[1], kind: m[2], name: m[3], value: m[4] }));
if (types.length !== 19) throw new Error('Unexpected association catalog; update generation validation');
const check = process.argv.includes('--check');
async function update(path, next) {
 const target = new URL(path, root), before = await readFile(target, 'utf8').catch(() => '');
 if (before.replaceAll('\r\n', '\n') === next) return;
 if (check) throw new Error(`${path} is stale; run node vue-app/scripts/build-system-associations.mjs`);
 await writeFile(target, before.includes('\r\n') ? next.replaceAll('\n', '\r\n') : next);
}
const schemes = types.filter(t => t.kind === 'scheme'), files = types.filter(t => t.kind === 'file');
const plistPath = 'app/build/darwin/Info.plist';
let plist = (await readFile(new URL(plistPath, root), 'utf8')).replaceAll('\r\n', '\n');
plist = plist.replace(/\n\s*<!-- external-open start -->[\s\S]*?<!-- external-open end -->\n/g, '');
const documents = '\n\t\t<!-- external-open start -->\n' + files.map(t => `<dict><key>CFBundleTypeName</key><string>${t.name}</string><key>CFBundleTypeRole</key><string>Viewer</string><key>LSHandlerRank</key><string>Alternate</string><key>CFBundleTypeExtensions</key><array><string>${t.value}</string></array></dict>`).join('\n') + '\n\t\t<!-- external-open end -->\n';
plist = plist.replace(/(<key>CFBundleDocumentTypes<\/key>[ \t]*\n[ \t]*<array>)/, '$1' + documents);
const urls = '\n\t\t<!-- external-open start -->\n' + schemes.map(t => `<dict><key>CFBundleURLName</key><string>com.mincode.external.${t.id}</string><key>CFBundleTypeRole</key><string>Viewer</string><key>CFBundleURLSchemes</key><array><string>${t.value}</string></array></dict>`).join('\n') + '\n\t\t<!-- external-open end -->\n';
plist = plist.replace(/(<key>CFBundleURLTypes<\/key>[ \t]*\n[ \t]*<array>)/, '$1' + urls);
await update(plistPath, plist);
// MSIX registers candidates, the OS still owns UserChoice.
const msixPath = 'app/build/windows/msix/app_manifest.xml';
let msix = (await readFile(new URL(msixPath, root), 'utf8')).replaceAll('\r\n', '\n').replace(/\n\s*<!-- external-open start -->[\s\S]*?<!-- external-open end -->\n/g, '');
const extensions = '\n        <!-- external-open start -->\n' + schemes.map(t => `<uap:Extension Category="windows.protocol"><uap:Protocol Name="${t.value}"><uap:DisplayName>${t.name}</uap:DisplayName></uap:Protocol></uap:Extension>`).join('\n') + '\n' + files.map(t => `<uap:Extension Category="windows.fileTypeAssociation"><uap:FileTypeAssociation Name="mc-${t.id}"><uap:DisplayName>${t.name}</uap:DisplayName><uap:SupportedFileTypes><uap:FileType>.${t.value}</uap:FileType></uap:SupportedFileTypes></uap:FileTypeAssociation></uap:Extension>`).join('\n') + '\n        <!-- external-open end -->\n';
msix = msix.replace(/(<Extensions>)/, '$1' + extensions);
await update(msixPath, msix);
// NSIS advertises capabilities, never overwrites .ext default values or UserChoice.
let nsis = '; Generated from externalopen/catalog.go. Do not edit.\n!macro mincode.externalCandidates\n  WriteRegStr HKCU "Software\\RegisteredApplications" "MinCode" "Software\\MinCode\\Capabilities"\n  WriteRegStr HKCU "Software\\MinCode\\Capabilities" "ApplicationName" "MinCode"\n';
for (const t of types) {
 const p = `MinCode.External.${t.id}`;
 nsis += `  WriteRegStr HKCU "Software\\Classes\\${p}" "" "${t.name}"\n  WriteRegStr HKCU "Software\\Classes\\${p}\\shell\\open\\command" "" '$\\"$INSTDIR\\\${PRODUCT_EXECUTABLE}$\\" $\\"%1$\\"'\n`;
 if (t.kind === 'scheme') nsis += `  WriteRegStr HKCU "Software\\Classes\\${p}" "URL Protocol" ""\n`;
 nsis += `  WriteRegStr HKCU "Software\\MinCode\\Capabilities\\${t.kind === 'file' ? 'FileAssociations' : 'URLAssociations'}" "${t.kind === 'file' ? '.' : ''}${t.value}" "${p}"\n`;
}
nsis += '!macroend\n!macro mincode.removeExternalCandidates\n';
for (const t of types) nsis += `  DeleteRegKey HKCU "Software\\Classes\\MinCode.External.${t.id}"\n`;
nsis += '  DeleteRegKey HKCU "Software\\MinCode\\Capabilities"\n  DeleteRegValue HKCU "Software\\RegisteredApplications" "MinCode"\n!macroend\n';
await update('app/build/windows/nsis/external_associations.nsh', nsis);
