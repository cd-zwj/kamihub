import { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { Card, Col, Empty, Input, Row, Skeleton, Space, Tag, Typography, message } from 'antd';
import { SearchOutlined, ShoppingOutlined } from '@ant-design/icons';
import { shopApi } from '@/shop/api/shopClient';
import type { ShopListing } from '@/shop/api/shopClient';
import StockTag, { isSoldOut } from '@/shop/components/StockTag';
import { formatMoney } from '@/shop/layout/ShopLayout';

const { Title, Text, Paragraph } = Typography;

export default function ShopHome() {
  const [list, setList] = useState<ShopListing[]>([]);
  const [loading, setLoading] = useState(true);
  const [keyword, setKeyword] = useState('');
  const navigate = useNavigate();

  const load = async (kw = '') => {
    setLoading(true);
    try {
      const res = await shopApi.listings({ page: 1, page_size: 60, keyword: kw || undefined });
      setList(res.list ?? []);
    } catch (e) {
      message.error(e instanceof Error ? e.message : '加载商品失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  return (
    <div>
      <div style={{ textAlign: 'center', marginBottom: 28 }}>
        <Title level={2} style={{ marginBottom: 8 }}>
          精选卡密 · 自动发货
        </Title>
        <Text type="secondary">下单后立即获取，支持随时查看订单与卡密</Text>
        <div style={{ marginTop: 20 }}>
          <Input
            size="large"
            allowClear
            prefix={<SearchOutlined />}
            placeholder="搜索商品"
            style={{ maxWidth: 420 }}
            value={keyword}
            onChange={(e) => setKeyword(e.target.value)}
            onPressEnter={() => load(keyword)}
          />
        </div>
      </div>

      {loading ? (
        <Row gutter={[16, 16]}>
          {[1, 2, 3, 4].map((i) => (
            <Col key={i} xs={24} sm={12} lg={8} xl={6}>
              <Card>
                <Skeleton active paragraph={{ rows: 3 }} />
              </Card>
            </Col>
          ))}
        </Row>
      ) : list.length === 0 ? (
        <Empty description="暂无在售商品" style={{ marginTop: 60 }} />
      ) : (
        <Row gutter={[16, 16]}>
          {list.map((item) => {
            const soldOut = isSoldOut(item.stock, item.stock_exact);
            return (
              <Col key={item.id} xs={24} sm={12} lg={8} xl={6}>
                <Card
                  hoverable={!soldOut}
                  styles={{ body: { padding: 16 } }}
                  onClick={() => !soldOut && navigate(`/listings/${item.id}`)}
                  cover={
                    item.cover_image ? (
                      <div
                        style={{
                          height: 150,
                          background: `#f0f2f5 url(${item.cover_image}) center/cover`,
                        }}
                      />
                    ) : (
                      <div
                        style={{
                          height: 150,
                          background: 'linear-gradient(135deg,#e8f0ff,#f5f7fa)',
                          display: 'flex',
                          alignItems: 'center',
                          justifyContent: 'center',
                        }}
                      >
                        <ShoppingOutlined style={{ fontSize: 46, color: '#a8c0ea' }} />
                      </div>
                    )
                  }
                >
                  <Space direction="vertical" size={6} style={{ width: '100%' }}>
                    <div style={{ display: 'flex', justifyContent: 'space-between', gap: 8 }}>
                      <Text strong ellipsis style={{ fontSize: 15, flex: 1 }}>
                        {item.title}
                      </Text>
                      {soldOut ? (
                        <Tag color="default">缺货</Tag>
                      ) : (
                        <StockTag
                          stock={item.stock}
                          stockExact={item.stock_exact}
                          stockUpdatedAt={item.stock_updated_at}
                        />
                      )}
                    </div>

                    <Paragraph
                      type="secondary"
                      ellipsis={{ rows: 2 }}
                      style={{ marginBottom: 0, minHeight: 44, fontSize: 13 }}
                    >
                      {item.subtitle || item.description || '暂无描述'}
                    </Paragraph>

                    <div style={{ display: 'flex', alignItems: 'baseline', gap: 8 }}>
                      <Text strong style={{ color: '#f5222d', fontSize: 20 }}>
                        ¥{formatMoney(item.sale_price)}
                      </Text>
                      {Number(item.origin_price) > Number(item.sale_price) && (
                        <Text delete type="secondary" style={{ fontSize: 12 }}>
                          ¥{formatMoney(item.origin_price)}
                        </Text>
                      )}
                    </div>

                    {item.sold_count > 0 && (
                      <Text type="secondary" style={{ fontSize: 12 }}>
                        已售 {item.sold_count}
                      </Text>
                    )}
                  </Space>
                </Card>
              </Col>
            );
          })}
        </Row>
      )}
    </div>
  );
}
