"use client";

import { createContext, useContext } from "react";

export type Me = {
  id: string;
  email: string;
  phone: string;
  full_name: string;
  roles: string[];
  status: string;
  mfa_enabled: boolean;
  last_login_at?: string;
};

export const MeContext = createContext<{ me?: Me; reload: () => void }>({ reload: () => {} });
export const useMe = () => useContext(MeContext);
