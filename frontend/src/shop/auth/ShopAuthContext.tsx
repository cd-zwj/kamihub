import { createContext, useCallback, useContext, useEffect, useMemo, useState } from 'react';
import type { ReactNode } from 'react';
import { shopApi, shopTokenStore, setShopUnauthorizedHandler } from '@/shop/api/shopClient';
import type { ShopBuyer } from '@/shop/api/shopClient';

/** 验证码答案：前端拿到题目后让用户填写，再连同 id 一起提交 */
export interface CaptchaInput {
  captcha_id: string;
  captcha_answer: string;
}

interface ShopAuthState {
  buyer: ShopBuyer | null;
  loading: boolean;
  login: (username: string, password: string, captcha?: CaptchaInput) => Promise<void>;
  register: (username: string, email: string, password: string, captcha?: CaptchaInput) => Promise<void>;
  logout: () => void;
  /** 刷新买家信息（下单/充值后余额会变，需要同步到顶栏） */
  refresh: () => Promise<void>;
}

const ShopAuthContext = createContext<ShopAuthState | null>(null);

export function ShopAuthProvider({ children }: { children: ReactNode }) {
  const [buyer, setBuyer] = useState<ShopBuyer | null>(null);
  const [loading, setLoading] = useState(true);

  // 令牌失效时清空买家信息，路由层会据此跳登录页
  useEffect(() => {
    setShopUnauthorizedHandler(() => setBuyer(null));
  }, []);

  // 首屏用已有令牌尝试恢复会话
  useEffect(() => {
    let alive = true;
    (async () => {
      if (!shopTokenStore.get()) {
        if (alive) setLoading(false);
        return;
      }
      try {
        const me = await shopApi.me();
        if (alive) setBuyer(me);
      } catch {
        shopTokenStore.clear();
      } finally {
        if (alive) setLoading(false);
      }
    })();
    return () => {
      alive = false;
    };
  }, []);

  const login = useCallback(
    async (username: string, password: string, captcha?: { captcha_id: string; captcha_answer: string }) => {
      const res = await shopApi.login(username, password, captcha);
      shopTokenStore.set(res.token);
      setBuyer(res.buyer);
    },
    [],
  );

  const register = useCallback(
    async (
      username: string,
      email: string,
      password: string,
      captcha?: { captcha_id: string; captcha_answer: string },
    ) => {
      const res = await shopApi.register({
        username,
        email: email || undefined,
        password,
        ...captcha,
      });
      shopTokenStore.set(res.token);
      setBuyer(res.buyer);
    },
    [],
  );

  const logout = useCallback(() => {
    shopApi.logout().catch(() => undefined);
    shopTokenStore.clear();
    setBuyer(null);
  }, []);

  const refresh = useCallback(async () => {
    try {
      setBuyer(await shopApi.me());
    } catch {
      // 刷新失败不打断当前操作
    }
  }, []);

  const value = useMemo(
    () => ({ buyer, loading, login, register, logout, refresh }),
    [buyer, loading, login, register, logout, refresh],
  );

  return <ShopAuthContext.Provider value={value}>{children}</ShopAuthContext.Provider>;
}

export function useShopAuth() {
  const ctx = useContext(ShopAuthContext);
  if (!ctx) throw new Error('useShopAuth 必须在 ShopAuthProvider 内使用');
  return ctx;
}
