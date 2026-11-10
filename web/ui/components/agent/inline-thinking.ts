/** Some OpenAI-compatible providers put reasoning tags in content instead of
 * the reasoning channel. Project them for display only; never rewrite history
 * sent back to the model. Markdown code examples remain ordinary content. */
export function projectInlineThinking(text: string, streaming: boolean) {
  const body: string[] = [], thoughts: string[] = [];
  let thinking = false, fence = '', inline = '', offset = 0;
  const append = (value: string) => (thinking ? thoughts : body).push(value);
  const tokens = /^ {0,3}(`{3,}|~{3,})[^\n]*|(`+)|<\/?think>/gm;
  for (const match of text.matchAll(tokens)) {
    const token = match[0], index = match.index!;
    append(text.slice(offset,index));
    offset = index + token.length;
    if (match[1]) {
      const marker = match[1];
      if (!fence && !inline) fence = marker;
      else if (fence && marker[0]===fence[0] && marker.length>=fence.length && token.trim()===marker) fence = '';
      append(token);
    } else if (fence) append(token);
    else if (match[2]) {
      if (!inline) inline = token;
      else if (inline===token) inline = '';
      append(token);
    } else if (inline || (index>0 && text[index-1]==='\\')) append(token);
    else if (token==='<think>' && !thinking) {
      if (thoughts.length) thoughts.push('\n\n');
      thinking = true;
    } else if (token==='</think>' && thinking) thinking = false;
    else append(token);
  }
  let tail = text.slice(offset);
  // Hold incomplete streaming delimiters rather than flashing '<thi' in text.
  if (streaming && !fence && !inline) {
    const tag = thinking ? '</think>' : '<think>';
    for (let length=Math.min(tag.length-1,tail.length); length>0; length--) {
      if (tail.endsWith(tag.slice(0,length))) { tail=tail.slice(0,-length);break; }
    }
  }
  append(tail);
  return {text:body.join(''), reasoning:thoughts.join('').trim(), thinking:streaming && thinking};
}
