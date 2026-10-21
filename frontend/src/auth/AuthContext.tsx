import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { api, clearConfirmTicket, setUnauthorizedHandler, tokenStore } from '@/api/client';
import type { User } from '@/api/types';

interface AuthState {
  user: User | null;
  loading: boolean;
  isAdmin: boolean;
  /**
   * 第一步：账号密码。
   *
   * 返回 needTotp=true 表示该账号开了两步验证 —— 此时**没有登录成功**，
   * 调用方必须接着走 loginTOTP；只有它成功才拿到真令牌。
   */
  login: (username: string, password: string) => Promise<{ needTotp: boolean; ticket?: string; setupRequired: boolean }>;
  /** 第二步：临时票据 + 动态码/恢复码。成功后才是真正登录 */
  loginTOTP: (ticket: string, code: string) => Promise<{ usedRecovery: boolean; hint?: string; setupRequired: boolean }>;
  logout: () => Promise<void>;
  refresh: () => Promise<void>;
}

const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    if (!tokenStore.get()) {
      setUser(null);
      setLoading(false);
      return;
    }
    try {
      setUser(await api.me());
    } catch {
      tokenStore.clear();
      setUser(null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    // 令牌失效时立刻清空本地状态，路由会自动跳登录页
    setUnauthorizedHandler(() => setUser(null));
    void refresh();
  }, [refresh]);

  const login = useCallback(async (username: string, password: string) => {
    const res = await api.login(username, password);
    // 开了 2FA：后端刻意不给 token，这里绝不能「当成功处理」
    if (res.need_totp) {
      return { needTotp: true, ticket: res.ticket, setupRequired: false };
    }
    tokenStore.set(res.token ?? '');
    if (res.user) setUser(res.user);
    return { needTotp: false, setupRequired: Boolean(res.totp_setup_required) };
  }, []);

  const loginTOTP = useCallback(async (ticket: string, code: string) => {
    const res = await api.loginTOTP(ticket, code);
    tokenStore.set(res.token ?? '');
    if (res.user) setUser(res.user);
    return {
      usedRecovery: Boolean(res.used_recovery_code),
      hint: res.recovery_hint,
      setupRequired: Boolean(res.totp_setup_required),
    };
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } catch {
      /* 后端不可达也要允许本地登出 */
    }
    tokenStore.clear();
    // 二次确认票据也一并作废：换人登录后不该继承上一个人的确认状态
    clearConfirmTicket();
    setUser(null);
  }, []);

  const value = useMemo<AuthState>(
    () => ({ user, loading, isAdmin: user?.role === 'admin', login, loginTOTP, logout, refresh }),
    [user, loading, login, loginTOTP, logout, refresh],
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

export function useAuth(): AuthState {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error('useAuth 必须在 AuthProvider 内使用');
  return ctx;
}
