import type { HttpRequestDraft } from "./request-collection";
import {
  buildHttpRequestBody,
  buildHttpRequestHeaders,
  buildHttpRequestUrl,
  serializeCurlCommand,
} from "./curl";

export type HttpCodeLanguage =
  | "curl"
  | "javascript"
  | "typescript"
  | "python"
  | "go"
  | "java"
  | "csharp"
  | "php"
  | "ruby"
  | "rust";

export const HTTP_CODE_LANGUAGES: { value: HttpCodeLanguage; label: string }[] = [
  { value: "curl", label: "cURL" },
  { value: "javascript", label: "JavaScript" },
  { value: "typescript", label: "TypeScript" },
  { value: "python", label: "Python" },
  { value: "go", label: "Go" },
  { value: "java", label: "Java" },
  { value: "csharp", label: "C#" },
  { value: "php", label: "PHP" },
  { value: "ruby", label: "Ruby" },
  { value: "rust", label: "Rust" },
];

function quoted(value: string) {
  return JSON.stringify(value);
}

function singleQuoted(value: string) {
  return `'${value.split("\\").join("\\\\").split("'").join("\\'")}'`;
}

function generateFetch(request: HttpRequestDraft, typescript = false) {
  const options = [`  method: ${quoted(request.method.toUpperCase())}`];
  const headers = buildHttpRequestHeaders(request);
  const body = buildHttpRequestBody(request);
  if (headers.length) {
    options.push(
      `  headers: {\n${headers.map(([key, value]) => `    ${quoted(key)}: ${quoted(value)}`).join(",\n")}\n  }`
    );
  }
  if (body !== undefined) options.push(`  body: ${quoted(body)}`);
  const responseDeclaration = typescript ? "const response: Response" : "const response";
  return `${responseDeclaration} = await fetch(${quoted(buildHttpRequestUrl(request))}, {\n${options.join(",\n")}\n});\n\nconst text = await response.text();\nconsole.log(response.status, text);`;
}

function generatePython(request: HttpRequestDraft) {
  const headers = buildHttpRequestHeaders(request);
  const body = buildHttpRequestBody(request);
  const argumentsList = [
    quoted(request.method.toUpperCase()),
    quoted(buildHttpRequestUrl(request)),
  ];
  if (headers.length) {
    argumentsList.push(
      `headers={\n${headers.map(([key, value]) => `        ${quoted(key)}: ${quoted(value)}`).join(",\n")}\n    }`
    );
  }
  if (body !== undefined) argumentsList.push(`data=${quoted(body)}`);
  return `import requests\n\nresponse = requests.request(\n    ${argumentsList.join(",\n    ")}\n)\n\nprint(response.status_code)\nprint(response.text)`;
}

function generateGo(request: HttpRequestDraft) {
  const requestBody = buildHttpRequestBody(request);
  const hasBody = requestBody !== undefined;
  const imports = ["fmt", "io", "log", "net/http", ...(hasBody ? ["strings"] : [])]
    .map((name) => `\t${quoted(name)}`)
    .join("\n");
  const body = hasBody ? `strings.NewReader(${quoted(requestBody)})` : "nil";
  const headerLines = buildHttpRequestHeaders(request)
    .map(([key, value]) => `\treq.Header.Set(${quoted(key)}, ${quoted(value)})`)
    .join("\n");

  return `package main\n\nimport (\n${imports}\n)\n\nfunc main() {\n\treq, err := http.NewRequest(${quoted(request.method.toUpperCase())}, ${quoted(buildHttpRequestUrl(request))}, ${body})\n\tif err != nil {\n\t\tlog.Fatal(err)\n\t}\n${headerLines ? `${headerLines}\n` : ""}\n\tresponse, err := http.DefaultClient.Do(req)\n\tif err != nil {\n\t\tlog.Fatal(err)\n\t}\n\tdefer response.Body.Close()\n\n\tpayload, err := io.ReadAll(response.Body)\n\tif err != nil {\n\t\tlog.Fatal(err)\n\t}\n\tfmt.Println(response.Status)\n\tfmt.Println(string(payload))\n}`;
}

function generateJava(request: HttpRequestDraft) {
  const requestBody = buildHttpRequestBody(request);
  const body = requestBody !== undefined
    ? `HttpRequest.BodyPublishers.ofString(${quoted(requestBody)})`
    : "HttpRequest.BodyPublishers.noBody()";
  const headerLines = buildHttpRequestHeaders(request)
    .map(([key, value]) => `            .header(${quoted(key)}, ${quoted(value)})`)
    .join("\n");

  return `import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;

public class Main {
    public static void main(String[] args) throws Exception {
        HttpRequest request = HttpRequest.newBuilder()
            .uri(URI.create(${quoted(buildHttpRequestUrl(request))}))
${headerLines ? `${headerLines}\n` : ""}            .method(${quoted(request.method.toUpperCase())}, ${body})
            .build();

        HttpResponse<String> response = HttpClient.newHttpClient()
            .send(request, HttpResponse.BodyHandlers.ofString());
        System.out.println(response.statusCode());
        System.out.println(response.body());
    }
}`;
}

function generateCSharp(request: HttpRequestDraft) {
  const requestBody = buildHttpRequestBody(request);
  const headerLines = buildHttpRequestHeaders(request)
    .map(
      ([key, value]) =>
        `request.Headers.TryAddWithoutValidation(${quoted(key)}, ${quoted(value)});`
    )
    .join("\n");
  const bodyLine = requestBody !== undefined
    ? `request.Content = new StringContent(${quoted(requestBody)}, Encoding.UTF8);`
    : "";

  return `using System;
using System.Net.Http;
using System.Text;

using var client = new HttpClient();
using var request = new HttpRequestMessage(new HttpMethod(${quoted(request.method.toUpperCase())}), ${quoted(buildHttpRequestUrl(request))});
${headerLines}${headerLines && bodyLine ? "\n" : ""}${bodyLine}

using var response = await client.SendAsync(request);
Console.WriteLine((int)response.StatusCode);
Console.WriteLine(await response.Content.ReadAsStringAsync());`;
}

function generatePhp(request: HttpRequestDraft) {
  const headers = buildHttpRequestHeaders(request);
  const body = buildHttpRequestBody(request);
  const options = [
    `    CURLOPT_URL => ${singleQuoted(buildHttpRequestUrl(request))}`,
    "    CURLOPT_RETURNTRANSFER => true",
    `    CURLOPT_CUSTOMREQUEST => ${singleQuoted(request.method.toUpperCase())}`,
  ];
  if (headers.length) {
    options.push(
      `    CURLOPT_HTTPHEADER => [\n${headers
        .map(([key, value]) => `        ${singleQuoted(`${key}: ${value}`)}`)
        .join(",\n")}\n    ]`
    );
  }
  if (body !== undefined) options.push(`    CURLOPT_POSTFIELDS => ${singleQuoted(body)}`);

  return `<?php
$handle = curl_init();
curl_setopt_array($handle, [
${options.join(",\n")}
]);

$body = curl_exec($handle);
$status = curl_getinfo($handle, CURLINFO_RESPONSE_CODE);
if ($body === false) {
    throw new RuntimeException(curl_error($handle));
}
curl_close($handle);

echo $status . PHP_EOL;
echo $body;`;
}

function generateRuby(request: HttpRequestDraft) {
  const body = buildHttpRequestBody(request);
  const headerLines = buildHttpRequestHeaders(request)
    .map(([key, value]) => `request[${singleQuoted(key)}] = ${singleQuoted(value)}`)
    .join("\n");
  const bodyLine = body !== undefined ? `request.body = ${singleQuoted(body)}` : "";

  return `require 'net/http'

uri = URI(${singleQuoted(buildHttpRequestUrl(request))})
http = Net::HTTP.new(uri.host, uri.port)
http.use_ssl = uri.scheme == 'https'
request = Net::HTTPGenericRequest.new(${singleQuoted(request.method.toUpperCase())}, ${body !== undefined}, true, uri.request_uri)
${headerLines}${headerLines && bodyLine ? "\n" : ""}${bodyLine}

response = http.request(request)
puts response.code
puts response.body`;
}

function generateRust(request: HttpRequestDraft) {
  const body = buildHttpRequestBody(request);
  const headerLines = buildHttpRequestHeaders(request)
    .map(([key, value]) => `        .header(${quoted(key)}, ${quoted(value)})`)
    .join("\n");
  const bodyLine = body !== undefined ? `\n        .body(${quoted(body)})` : "";

  return `fn main() -> Result<(), Box<dyn std::error::Error>> {
    let client = reqwest::blocking::Client::new();
    let response = client
        .request(reqwest::Method::from_bytes(${quoted(request.method.toUpperCase())}.as_bytes())?, ${quoted(buildHttpRequestUrl(request))})
${headerLines ? `${headerLines}\n` : ""}${bodyLine}
        .send()?;

    let status = response.status();
    let body = response.text()?;
    println!("{}", status);
    println!("{}", body);
    Ok(())
}`;
}

export function generateHttpCode(request: HttpRequestDraft, language: HttpCodeLanguage) {
  if (language === "curl") return serializeCurlCommand(request);
  if (language === "javascript") return generateFetch(request);
  if (language === "typescript") return generateFetch(request, true);
  if (language === "python") return generatePython(request);
  if (language === "go") return generateGo(request);
  if (language === "java") return generateJava(request);
  if (language === "csharp") return generateCSharp(request);
  if (language === "php") return generatePhp(request);
  if (language === "ruby") return generateRuby(request);
  return generateRust(request);
}
