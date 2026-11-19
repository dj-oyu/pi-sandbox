import { marked } from 'marked';
import DOMPurify from 'dompurify';

marked.setOptions({
  breaks: true,
  gfm: true
});

export function renderMarkdown(src: string): string {
  if (!src) return '';
  const rawHtml = marked.parse(src, { async: false }) as string;
  return DOMPurify.sanitize(rawHtml);
}
