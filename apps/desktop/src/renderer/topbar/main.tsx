// Shell page "topbar" (docs/specs/desktop.md 3.4): the 48 px bar of the main window.
import { mountPage } from '../shared/mount';
import { TopBar } from './TopBar';

mountPage(<TopBar bridge={window.localaiDesktop} />);
