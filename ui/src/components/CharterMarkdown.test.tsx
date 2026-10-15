import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { describe, expect, it } from 'vitest';

import { CharterMarkdown } from './CharterMarkdown';

// Charter text reaches this component from the backend's /charter-preview
// endpoint, which sources it from a ValidMind custom field — remote content on
// the far side of a different trust boundary. These tests pin the property that
// makes rendering it safe: nothing in that text can become live markup.
const render = (text: string) => renderToStaticMarkup(<CharterMarkdown text={text} />);

describe('CharterMarkdown rendering', () => {
  it('renders headings, lists and emphasis as elements', () => {
    const html = render('# Scope\n\n- no **prod** writes\n- read-only SQL\n');
    expect(html).toContain('Scope');
    expect(html).toContain('<li');
    expect(html).toContain('<strong');
  });

  it('renders fenced code blocks in a pre', () => {
    expect(render('```\nSELECT 1\n```\n')).toContain('<pre');
  });

  it('renders GFM tables', () => {
    const html = render('| tool | allowed |\n| --- | --- |\n| sql | read |\n');
    expect(html).toContain('<table');
    expect(html).toContain('<td');
  });
});

describe('CharterMarkdown untrusted input', () => {
  it('escapes script tags in the source instead of executing them', () => {
    const html = render('Allowed: reads.\n\n<script>alert(1)</script>\n');
    expect(html).not.toContain('<script>');
    expect(html).toContain('&lt;script&gt;');
  });

  it('escapes event handlers rather than attaching them', () => {
    const html = render('<img src=x onerror="alert(1)">\n');
    expect(html).not.toContain('<img');
    expect(html).toContain('&lt;img');
  });

  it('escapes iframes rather than embedding them', () => {
    const html = render('<iframe src="https://evil.example"></iframe>\n');
    expect(html).not.toContain('<iframe');
    expect(html).toContain('&lt;iframe');
  });

  it('strips javascript: URLs from markdown links', () => {
    const html = render('[click](javascript:alert(1))\n');
    expect(html).not.toContain('javascript:');
  });

  it('strips data: URLs from markdown links', () => {
    const html = render('[click](data:text/html;base64,PHNjcmlwdD4=)\n');
    expect(html).not.toContain('data:text/html');
  });

  it('keeps ordinary https links and marks them noopener', () => {
    const html = render('[policy](https://example.com/policy)\n');
    expect(html).toContain('https://example.com/policy');
    expect(html).toContain('noopener');
  });

  // Regression guard: `skipHtml` would make this pass the "no live markup" bar
  // while silently deleting the table name from the rendered clause.
  it('keeps angle-bracketed names visible in plain charter prose', () => {
    const html = render('Deny writes to <customers> unless approved.\n');
    expect(html).not.toContain('<customers>');
    expect(html).toContain('&lt;customers&gt;');
  });

  it('never emits a stray node attribute from react-markdown internals', () => {
    expect(render('# Scope\n\ntext\n')).not.toContain('node=');
  });
});
