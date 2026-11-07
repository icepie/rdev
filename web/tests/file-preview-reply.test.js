import { expect, it } from 'vitest';
import { readFilePreviewReply } from '../ui/lib/file-preview-reply';

it('rejects obsolete, closed, other-device and unrelated preview replies', () => {
  const reply = {op:'file_content',requestId:'new',deviceId:'one',content:'current'};
  expect(readFilePreviewReply('old','one',reply)).toBeNull();
  expect(readFilePreviewReply('','one',reply)).toBeNull();
  expect(readFilePreviewReply('new','two',reply)).toBeNull();
  expect(readFilePreviewReply('new','one',{...reply,op:'list_result'})).toBeNull();
});
it('preserves bounded text and image metadata and reports read failures', () => {
  const reply = {op:'file_content',requestId:'id',deviceId:'one'};
  expect(readFilePreviewReply('id','one',{...reply,content:'text',truncated:true})).toMatchObject({status:'ready',content:'text',truncated:true});
  expect(readFilePreviewReply('id','one',{...reply,content:'aW1hZ2U=',encoding:'base64',mime:'image/png'})).toMatchObject({status:'ready',encoding:'base64',mime:'image/png'});
  expect(readFilePreviewReply('id','one',{...reply,message:'binary file'})).toMatchObject({status:'error',message:'binary file'});
  expect(readFilePreviewReply('id','one',{...reply,op:'error',message:'offline'})).toMatchObject({status:'error',message:'offline'});
});
