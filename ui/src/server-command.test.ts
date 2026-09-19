// A stdio server's launch line (flanj.mcp.server.command, 2026-09-18) and the
// `unknown` edge class (Python SDK) — the pure halves. The mounted card and the
// Flag sheet are in server-command-card.test.ts.
import { describe, expect, it } from 'vitest';
import { serverCommandLine, MCP_LAUNCHED_AS } from './mcp';
import { contractHeading, contractOrigin, ORIGIN_UNKNOWN, type ContractSpec } from './contracts';

describe('serverCommandLine', () => {
  it('joins the argv with single spaces behind the label', () => {
    expect(MCP_LAUNCHED_AS).toBe('Launched as:');
    expect(serverCommandLine('local-process', '["npx","-y","@stripe/mcp@0.2.1"]')).toBe(
      'Launched as: npx -y @stripe/mcp@0.2.1'
    );
  });

  it('quotes an element that contains whitespace, or is empty, so argv boundaries stay legible', () => {
    expect(serverCommandLine('local-process', '["node","/opt/My Tools/server.js","--name",""]')).toBe(
      'Launched as: node "/opt/My Tools/server.js" --name ""'
    );
    expect(serverCommandLine('local-process', '["sh","-c","echo \\"hi\\"\\tthere"]')).toBe(
      'Launched as: sh -c "echo \\"hi\\"\\tthere"'
    );
  });

  it('keeps the cap marker and non-ASCII as they arrived', () => {
    expect(serverCommandLine('local-process', '["uvx","acme-mcp","--profile","café","…"]')).toBe(
      'Launched as: uvx acme-mcp --profile café …'
    );
  });

  it('renders nothing for any class but local-process', () => {
    const cmd = '["npx","-y","@stripe/mcp@0.2.1"]';
    for (const cls of ['external', 'internal', 'unknown', '', undefined, 'something-new']) {
      expect(serverCommandLine(cls, cmd)).toBe('');
    }
  });

  it('renders nothing when the field is absent or is not a non-empty JSON array of strings', () => {
    for (const bad of [undefined, '', 'npx -y @stripe/mcp', '"npx"', '{"command":"npx"}', '["npx",1]', '["npx",null]', 'null', '[]', '["npx"']) {
      expect(serverCommandLine('local-process', bad)).toBe('');
    }
  });
});

describe('the `unknown` edge class', () => {
  const unknown: ContractSpec = {
    integration: 'acme-py',
    role: 'provider',
    peer_host: 'acme-py-mcp', // the Python SDK puts serverInfo.name here for `unknown`
    format: 'mcp',
    title: 'acme-py-mcp',
    edge_class: 'unknown'
  };

  it('says the location is unknown instead of repeating the server name as a place', () => {
    expect(ORIGIN_UNKNOWN).toBe('location unknown');
    expect(contractOrigin(unknown)).toBe('location unknown');
    expect(contractHeading(unknown)).toBe('acme-py-mcp · location unknown');
  });

  it('leaves the other classes where they were', () => {
    expect(contractOrigin({ ...unknown, edge_class: 'local-process' })).toBe('stdio');
    expect(contractOrigin({ ...unknown, edge_class: 'external', peer_host: 'mcp.acme.test' })).toBe('mcp.acme.test');
    // A class nobody has defined yet falls back to the host, as before.
    expect(contractOrigin({ ...unknown, edge_class: 'something-new', peer_host: 'mcp.acme.test' })).toBe('mcp.acme.test');
  });
});
