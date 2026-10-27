import { createContext } from 'react'
import type { AccountRow } from '../types'

/** Mobile cells request usage only after entering the source's 200px margin. */
export const AccountQuotaRefreshContext = createContext<((account: AccountRow) => void) | null>(null)
