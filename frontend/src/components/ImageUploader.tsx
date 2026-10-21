import { useState } from 'react';
import { Button, Input, Space, Typography, Upload, message } from 'antd';
import { DeleteOutlined, LoadingOutlined, UploadOutlined } from '@ant-design/icons';
import type { UploadProps } from 'antd';
import http from '@/api/client';

const { Text } = Typography;

interface Props {
  /** 由 Form.Item 注入 */
  value?: string;
  onChange?: (v: string) => void;
  /** 输入框下方的提示 */
  hint?: string;
}

/** 服务端允许的格式与大小，与后端 handler_upload.go 保持一致 */
const ACCEPT = 'image/png,image/jpeg,image/webp,image/gif';
const MAX_MB = 5;

/**
 * 商品图片上传器。
 *
 * 直接传文件而不是填 URL —— 让运营去别处图床传完再复制链接太别扭。
 *
 * 同时保留「手动填 URL」入口：有些图本来就放在外部 CDN 上，
 * 强制上传反而多一道搬运。两者共用同一个字段。
 *
 * 组件是受控的（value/onChange），可以直接放进 Form.Item。
 */
export default function ImageUploader({ value, onChange, hint }: Props) {
  const [uploading, setUploading] = useState(false);

  const customRequest: UploadProps['customRequest'] = async (options) => {
    const file = options.file as File;

    // 前端先做一次大小检查，省掉一次无谓的请求；
    // 真正的把关在后端（前端的检查可以绕过）。
    if (file.size > MAX_MB * 1024 * 1024) {
      message.error(`图片不能超过 ${MAX_MB} MB`);
      options.onError?.(new Error('文件过大'));
      return;
    }

    const form = new FormData();
    form.append('file', file);

    setUploading(true);
    try {
      const res = await http.post('/upload/image', form, {
        headers: { 'Content-Type': 'multipart/form-data' },
      });
      const payload = res.data as { code: number; msg: string; data: { url: string } };
      if (payload.code !== 1) {
        throw new Error(payload.msg || '上传失败');
      }
      onChange?.(payload.data.url);
      message.success('图片已上传');
      options.onSuccess?.(payload);
    } catch (e) {
      message.error(e instanceof Error ? e.message : '上传失败');
      options.onError?.(e as Error);
    } finally {
      setUploading(false);
    }
  };

  return (
    <Space direction="vertical" size={8} style={{ width: '100%' }}>
      <Space align="start" size={12} wrap>
        {/* 预览：有图才显示，避免空占位干扰 */}
        {value ? (
          <div
            style={{
              width: 88,
              height: 88,
              borderRadius: 8,
              border: '1px solid #e8ecf3',
              background: `#fafbfe url(${value}) center/cover no-repeat`,
              flexShrink: 0,
            }}
          />
        ) : (
          <div
            style={{
              width: 88,
              height: 88,
              borderRadius: 8,
              border: '1px dashed #d9dfe8',
              background: '#fafbfe',
              display: 'flex',
              alignItems: 'center',
              justifyContent: 'center',
              color: '#b8c0cc',
              fontSize: 12,
              flexShrink: 0,
            }}
          >
            无图
          </div>
        )}

        <Space direction="vertical" size={6}>
          <Upload
            accept={ACCEPT}
            showUploadList={false}
            customRequest={customRequest}
            disabled={uploading}
          >
            <Button icon={uploading ? <LoadingOutlined /> : <UploadOutlined />} loading={uploading}>
              {uploading ? '上传中…' : '上传图片'}
            </Button>
          </Upload>

          {value && (
            <Button size="small" type="text" danger icon={<DeleteOutlined />} onClick={() => onChange?.('')}>
              移除
            </Button>
          )}
        </Space>
      </Space>

      {/* 也允许直接填外链 */}
      <Input
        value={value}
        onChange={(e) => onChange?.(e.target.value)}
        placeholder="也可以直接粘贴图片 URL"
        allowClear
      />

      <Text type="secondary" style={{ fontSize: 12 }}>
        {hint ?? `支持 JPG / PNG / WebP / GIF，单张不超过 ${MAX_MB} MB。`}
      </Text>
    </Space>
  );
}
