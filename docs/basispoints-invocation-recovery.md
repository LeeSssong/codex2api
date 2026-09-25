# Basispoints invocation recovery

The bridge accepts one complete JSON tool envelope or one complete declared tool
invocation. The invocation's callee selects the tool; fields named `name`, `tool`,
`args`, or `arguments` inside its argument remain ordinary client data.

Supported invocation forms include:

```javascript
functions.shell({"command":"pwd"})
await functions.shell({"command":"pwd"});
return await functions.shell({"command":"pwd"} ) ;
functions.exec("text(\"hello\");\n");
```

A function requires one JSON object, and a custom tool requires one JSON string.
The bridge does not execute JavaScript. It rejects assignments, unknown callees,
multiple calls, multiple arguments, incomplete calls, and trailing program text.
Large JSON integers retain their exact value. Complete JSON envelopes, supported
Markdown fences, and short prose labels continue through the existing decoder.

Raw custom input remains available through the explicit
`codex2api.custom/<catalog-name>` summary marker. An ordinary summary mentioning a
tool, or an unmarked patch body, no longer selects a custom tool. Object-valued
custom input is rejected instead of unwrapping a guessed property or serializing
it into code. Clients receive `basispoints_protocol_error` for malformed calls.

Successful recovery keeps the original native item for replay. Generated function
calls retain `encrypted_function_args: []` for plaintext collaboration messages.
Invalid responses do not emit partial client tool events, even when another valid
tool appeared earlier in the response.

Regression coverage includes the 14 comparison fixtures, exact custom strings,
namespace identity, multi-turn replay, collaboration messages, invalid SSE tool
responses, and the registered HTTP ingress with full or cached history. These
tests use synthetic responses and do not establish live account availability or
model quality.
